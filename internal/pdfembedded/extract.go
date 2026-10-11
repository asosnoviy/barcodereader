// Package pdfembedded реализует стадию A пайплайна: извлечение растровых
// изображений, встроенных в PDF, без растеризации страниц.
package pdfembedded

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	_ "golang.org/x/image/tiff" // TIFF/CCITT-сканы отсутствуют в stdlib

	"barcodereader/internal/apierrors"
)

// pdfcpu по умолчанию ищет свой конфиг в пользовательском каталоге и
// создаёт его, если нет. В контейнере под non-root пользователем это
// падает с «mkdir /.config: permission denied»: домашнего каталога нет,
// а fault.Fail в pdfcpu поднимает панику.
//
// Сервису пользовательский конфиг pdfcpu не нужен: мы сами задаём все
// параметры. Поэтому каталог отключается один раз при инициализации,
// а собственные вызовы идут через stateless-конфигурацию, которая не
// обращается к файловой системе вовсе.
func init() {
	api.DisableConfigDir()
}

// Image — картинка, извлечённая из PDF.
type Image struct {
	// Img — декодированное изображение.
	Img image.Image
	// PageNr — номер страницы в документе, начиная с 1.
	PageNr int
	// ObjNr — номер объекта PDF; попадает в ответ как image_object.
	ObjNr int
	// FileType — расширение исходного потока ("jpg", "png", "tiff", ...).
	FileType string
}

// minImageSide отсекает крошечные картинки: иконки, разделители и прочие
// элементы оформления, в которых штрихкода быть не может. Это минимальная
// сторона, а не площадь.
const minImageSide = 32

// Extractor реализует стадию A как метод, чтобы пакет можно было подставить
// в decode.Service без обёрток в main.
type Extractor struct{}

// Extract извлекает встроенные изображения указанных страниц.
func (Extractor) Extract(ctx context.Context, pdf []byte, selection []int, maxPages int) ([]Image, error) {
	return Extract(ctx, pdf, selection, maxPages)
}

// PageCount возвращает число страниц документа. Используется для meta.pages_total,
// когда стадия рендера не запускалась.
func (Extractor) PageCount(ctx context.Context, pdf []byte) (int, error) {
	return PageCount(ctx, pdf)
}

// PageCount возвращает число страниц документа.
func PageCount(ctx context.Context, pdf []byte) (int, error) {
	count, err := api.PageCount(ctx, bytes.NewReader(pdf), newConfiguration())
	if err != nil {
		return 0, classify(err)
	}
	return count, nil
}

// newConfiguration собирает конфигурацию pdfcpu.
//
// Используется stateless-вариант: он не читает и не создаёт config.yml,
// что обязательно в контейнере под non-root. Все нужные параметры
// выставляются явно.
func newConfiguration() *model.Configuration {
	conf := model.NewStatelessConfiguration()
	// JBIG2 и прочие неподдерживаемые потоки не должны ронять стадию целиком.
	conf.UnsupportedResourcePolicy = model.UnsupportedResourceSkip
	return conf
}

// Extract достаёт встроенные изображения документа.
//
// selection — номера страниц (1-based); пустой список означает «первые maxPages».
// Ошибки делятся на две группы: нарушения формата (битый или защищённый PDF)
// возвращаются как apierrors, а всё остальное — предупреждением, после
// которого вызывающий код переходит к стадии рендера.
func Extract(ctx context.Context, pdf []byte, selection []int, maxPages int) (images []Image, err error) {
	// pdfcpu местами паникует на неподдерживаемых потоках (например, на
	// некоторых PNG). Для сервиса это не повод падать: перехватываем и
	// спускаемся к стадии рендера.
	defer func() {
		if recovered := recover(); recovered != nil {
			images = nil
			err = fmt.Errorf("%w: pdfcpu паника: %v", ErrStageFailed, recovered)
		}
	}()

	if maxPages < 1 {
		maxPages = 1
	}

	pageNumbers := make([]string, 0, maxPages)
	if len(selection) == 0 {
		for i := 1; i <= maxPages; i++ {
			pageNumbers = append(pageNumbers, strconv.Itoa(i))
		}
	} else {
		for _, page := range selection {
			if page < 1 {
				continue
			}
			if len(pageNumbers) >= maxPages {
				break
			}
			pageNumbers = append(pageNumbers, strconv.Itoa(page))
		}
	}
	if len(pageNumbers) == 0 {
		return nil, nil
	}

	conf := newConfiguration()

	pages, extractErr := api.ExtractImagesRaw(ctx, bytes.NewReader(pdf), pageNumbers, conf)

	// При политике Skip pdfcpu возвращает UnsupportedResourceError вместе с
	// успешно извлечёнными картинками. Отбрасывать результат в этом случае
	// нельзя — картинки вполне рабочие.
	if extractErr != nil && len(pages) == 0 {
		return nil, classify(extractErr)
	}
	if extractErr != nil {
		slog.Debug("часть встроенных изображений не извлечена", "err", extractErr)
	}

	out, err := decodeAll(ctx, pages, pageNumbers)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// decodeAll превращает потоки pdfcpu в изображения. pageNumbers — список
// номеров страниц, соответствующий позициям в pages; он нужен, если pdfcpu
// не проставил PageNr.
func decodeAll(ctx context.Context, pages []map[int]model.Image, pageNumbers []string) ([]Image, error) {
	var out []Image
	for position, pageImages := range pages {
		if err := ctx.Err(); err != nil {
			return nil, apierrors.Timeout()
		}
		for _, raw := range pageImages {
			if raw.Reader == nil {
				continue
			}

			img, format, err := decodeImage(raw)
			if err != nil || img == nil {
				continue
			}

			bounds := img.Bounds()
			if bounds.Dx() < minImageSide || bounds.Dy() < minImageSide {
				continue
			}

			pageNr := raw.PageNr
			if pageNr <= 0 && position < len(pageNumbers) {
				pageNr, _ = strconv.Atoi(pageNumbers[position])
			}
			if pageNr <= 0 {
				pageNr = position + 1
			}

			fileType := raw.FileType
			if fileType == "" {
				fileType = format
			}

			out = append(out, Image{
				Img:      img,
				PageNr:   pageNr,
				ObjNr:    raw.ObjNr,
				FileType: fileType,
			})
		}
	}
	return out, nil
}

// decodeImage декодирует поток изображения. Начиная с Go 1.27 image.Decode
// дополнительно возвращает имя распознанного формата.
func decodeImage(raw model.Image) (image.Image, string, error) {
	return image.Decode(raw.Reader)
}

// ErrStageFailed — «мягкая» ошибка стадии A: документ при этом может быть
// вполне читаемым, просто встроенные картинки извлечь не удалось.
// Оркестратор логирует её и переходит к стадии рендера.
var ErrStageFailed = errors.New("извлечение встроенных изображений не удалось")

// classify отделяет «документ непригоден» от «стадия не сработала».
func classify(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "encrypted", "password", "crypt"):
		return apierrors.PDFEncrypted(err)
	case containsAny(msg, "corrupt", "damaged", "invalid", "malformed", "cannot read", "can't read"):
		return apierrors.PDFUnreadable(err)
	default:
		// Непонятная ошибка не должна блокировать стадию рендера: там
		// pdfcpu не участвует, и документ может оказаться вполне читаемым.
		return fmt.Errorf("%w: %w", ErrStageFailed, err)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}