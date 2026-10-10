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
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	_ "golang.org/x/image/tiff" // TIFF/CCITT-сканы отсутствуют в stdlib

	"barcodereader/internal/apierrors"
)

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

// minImagePixels отсекает крошечные картинки: иконки, разделители и прочие
// элементы оформления, в которых штрихкода быть не может.
const minImagePixels = 32 * 32

// Extractor реализует стадию A как метод, чтобы пакет можно было подставить
// в decode.Service без обёрток в main.
type Extractor struct{}

// Extract извлекает встроенные изображения первых maxPages страниц.
func (Extractor) Extract(ctx context.Context, pdf []byte, maxPages int) ([]Image, error) {
	return Extract(ctx, pdf, maxPages)
}

// Extract достаёт встроенные изображения первых maxPages страниц документа.
//
// Ошибки делятся на две группы: нарушения формата (битый или защищённый PDF)
// возвращаются как apierrors, а всё остальное — предупреждением, после
// которого вызывающий код переходит к стадии рендера.
func Extract(ctx context.Context, pdf []byte, maxPages int) ([]Image, error) {
	if maxPages < 1 {
		maxPages = 1
	}

	selection := make([]string, 0, maxPages)
	for i := 1; i <= maxPages; i++ {
		selection = append(selection, fmt.Sprintf("%d", i))
	}

	conf := model.NewDefaultConfiguration()
	// JBIG2 и прочие неподдерживаемые потоки не должны ронять стадию целиком.
	conf.UnsupportedResourcePolicy = model.UnsupportedResourceSkip

	pages, err := api.ExtractImagesRaw(ctx, bytes.NewReader(pdf), selection, conf)
	if err != nil {
		return nil, classify(err)
	}

	var out []Image
	for position, pageImages := range pages {
		for _, raw := range pageImages {
			if raw.Reader == nil {
				continue
			}

			img, format, err := decodeImage(raw)
			if err != nil || img == nil {
				continue
			}

			bounds := img.Bounds()
			if bounds.Dx() < minImagePixels || bounds.Dy() < minImagePixels {
				continue
			}

			pageNr := raw.PageNr
			if pageNr <= 0 {
				// Индекс элемента соответствует позиции в выборке страниц.
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