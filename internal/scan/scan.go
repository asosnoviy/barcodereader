// Package scan декодирует QR-коды и линейные штрихкоды из растровых изображений
// на базе ZXing (порт gozxing).
package scan

import (
	"context"
	"image"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/datamatrix"
	zxingmultiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
	zxingqr "github.com/makiuchi-d/gozxing/qrcode"
)

// Kind различает QR-коды и линейные штрихкоды в едином массиве codes[].
type Kind string

const (
	KindQR      Kind = "qr"
	KindBarcode Kind = "barcode"
	// KindMatrix — двумерный код Data Matrix. На этикетках маркетплейсов
	// и в логистике он встречается часто, но в QR он не превращается.
	KindMatrix Kind = "matrix"
)

// Code — найденный код. Точки лежат в пикселях исходного изображения, в
// котором производился поиск.
type Code struct {
	Value  string
	Kind   Kind
	Format string
	Points []image.Point
}

// Options задаёт поведение сканирования одного изображения.
type Options struct {
	// MaxImageDim ограничивает сторону изображения перед поиском QR.
	MaxImageDim int
	// OneDMaxWidth ограничивает ширину копии, на которой ищутся линейные коды.
	OneDMaxWidth int
	// IncludeBarcodes включает поиск линейных штрихкодов.
	IncludeBarcodes bool
	// ReturnAllQR включает поиск всех QR на изображении, а не только первого.
	ReturnAllQR bool
}

// qrAttempt описывает одну попытку поиска QR-кода.
type qrAttempt struct {
	name string
	kind attemptKind
	// hints дополняет базовые подсказки декодера.
	hints map[gozxing.DecodeHintType]any
}

var baseQRHints = map[gozxing.DecodeHintType]any{
	gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{gozxing.BarcodeFormat_QR_CODE},
}

func tryHarderHints() map[gozxing.DecodeHintType]any {
	return map[gozxing.DecodeHintType]any{
		gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{gozxing.BarcodeFormat_QR_CODE},
		gozxing.DecodeHintType_TRY_HARDER:        true,
	}
}

func invertedHints() map[gozxing.DecodeHintType]any {
	return map[gozxing.DecodeHintType]any{
		gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{gozxing.BarcodeFormat_QR_CODE},
		gozxing.DecodeHintType_TRY_HARDER:        true,
		gozxing.DecodeHintType_ALSO_INVERTED:     true,
	}
}

// attemptKind — способ построения бинарного представления попытки.
type attemptKind int

const (
	attemptHybrid attemptKind = iota
	attemptGlobal
	attemptInverted
)

// bitmap строит представление поверх разделяемого источника яркости:
// яркость считается один раз, а бинаризатор у каждой попытки свой —
// иначе кеш матрицы смешал бы результаты разных попыток.
func (a qrAttempt) bitmap(lum *luminance) (*gozxing.BinaryBitmap, error) {
	switch a.kind {
	case attemptGlobal:
		return lum.global()
	case attemptInverted:
		return lum.inverted()
	default:
		return lum.hybrid()
	}
}

// qrAttempts — лестница попыток: от дешёвой и стандартной к более дорогим.
func qrAttempts() []qrAttempt {
	return []qrAttempt{
		{name: "hybrid", kind: attemptHybrid, hints: baseQRHints},
		{name: "hybrid+harder", kind: attemptHybrid, hints: tryHarderHints()},
		{name: "global", kind: attemptGlobal, hints: baseQRHints},
		{name: "inverted", kind: attemptInverted, hints: invertedHints()},
	}
}

// Scan ищет QR-коды и линейные штрихкоды на изображении.
//
// Возвращает коды в порядке обнаружения; дубликаты по значению внутри одного
// изображения не удаляются — это делает оркестратор (internal/decode), который
// знает номер страницы и источник находки.
func Scan(ctx context.Context, img image.Image, opts Options) []Code {
	if img == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	found := make([]Code, 0, 2)
	found = append(found, scanQR(ctx, img, opts)...)
	found = append(found, scanDataMatrix(ctx, img, opts)...)
	if opts.IncludeBarcodes && ctx.Err() == nil {
		found = append(found, scanOneD(ctx, img, opts)...)
	}
	return found
}

// scanDataMatrix ищет двумерные коды Data Matrix.
//
// Ридер ZXing на странице целиком часто не находит символ: его детектор
// начинает осмотр от центра кадра (см. regions.go). Поэтому поиск идёт двумя
// попытками — обычной и с TRY_HARDER, — а для символов, стоящих не по центру,
// оркестратор дополнительно проходит по тёмным областям (ScanRegions).
func scanDataMatrix(ctx context.Context, img image.Image, opts Options) []Code {
	scaled := Downscale(img, opts.MaxImageDim)

	attempts := []map[gozxing.DecodeHintType]any{
		{gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{gozxing.BarcodeFormat_DATA_MATRIX}},
		{
			gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{gozxing.BarcodeFormat_DATA_MATRIX},
			gozxing.DecodeHintType_TRY_HARDER:        true,
		},
	}

	lum := newLuminance(scaled)

	for _, hints := range attempts {
		if err := ctx.Err(); err != nil {
			return nil
		}
		bitmap, err := lum.hybrid()
		if err != nil {
			return nil
		}
		res, err := datamatrix.NewDataMatrixReader().Decode(bitmap, hints)
		if err == nil && res != nil {
			return convertResults([]*gozxing.Result{res}, KindMatrix)
		}
	}
	return nil
}

// scanQR перебирает попытки, пока не найдёт QR-код(ы).
func scanQR(ctx context.Context, img image.Image, opts Options) []Code {
	scaled := Downscale(img, opts.MaxImageDim)
	lum := newLuminance(scaled)

	for _, attempt := range qrAttempts() {
		if err := ctx.Err(); err != nil {
			return nil
		}
		bitmap, err := attempt.bitmap(lum)
		if err != nil {
			continue
		}

		if opts.ReturnAllQR {
			results, err := zxingmultiqr.NewQRCodeMultiReader().DecodeMultiple(bitmap, attempt.hints)
			if err == nil && len(results) > 0 {
				return convertResults(results, KindQR)
			}
			// Множественный поиск может не найти код, который виден одиночному,
			// поэтому не прерываем лестницу.
		}

		result, err := zxingqr.NewQRCodeReader().Decode(bitmap, attempt.hints)
		if err == nil && result != nil {
			return convertResults([]*gozxing.Result{result}, KindQR)
		}
	}
	return nil
}

// scanOneD ищет линейные штрихкоды на уменьшенной копии: построчный перебор
// строк на полноразмерной странице — самый дорогой путь во всём сервисе.
func scanOneD(ctx context.Context, img image.Image, opts Options) []Code {
	small := DownscaleWidth(ToGray(img), opts.OneDMaxWidth)
	if small == nil {
		return nil
	}

	// Яркость и бинаризация считаются один раз на все три попытки: картинка
	// одна и та же, а ZXing пересчитывает и то и другое при каждом вызове.
	bitmap, err := newLuminance(small).hybrid()
	if err != nil {
		return nil
	}

	for _, hintSet := range []map[gozxing.DecodeHintType]any{
		nil,
		{gozxing.DecodeHintType_TRY_HARDER: true},
		// PURE_BARCODE оправдан только последней попыткой: он заставляет ZXing
		// смотреть только центральную строку и терпит неудачу на целой странице.
		{gozxing.DecodeHintType_PURE_BARCODE: true},
	} {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if codes := tryOneDReaders(bitmap, hintSet); len(codes) > 0 {
			// Попытка могла вернуть мусор: вертикальную линию рамки ZXing
			// принимает за штрихкод. Отфильтрованное пусто означает, что
			// надо продолжать лестницу, а не останавливаться.
			if kept := filterOneD(codes); len(kept) > 0 {
				return kept
			}
		}
	}

	// Штрихкод может быть напечатан белым по чёрному — так его делают, например,
	// на этикетках маркетплейсов. Обычные попытки выше такой код не видят,
	// поэтому последней идёт попытка на инвертированном изображении.
	if err := ctx.Err(); err != nil {
		return nil
	}
	inverted := DownscaleWidth(Invert(img), opts.OneDMaxWidth)
	if inverted == nil {
		return nil
	}
	invertedBitmap, err := newLuminance(inverted).hybrid()
	if err != nil {
		return nil
	}
	return filterOneD(tryOneDReaders(invertedBitmap, map[gozxing.DecodeHintType]any{
		gozxing.DecodeHintType_TRY_HARDER: true,
	}))
}

// minBarcodeWidth — минимальная ширина линейного кода в пикселях.
//
// Нужен, чтобы отсечь ложные срабатывания. ZXing при TRY_HARDER
// соглашается на вертикальные линии таблиц и рамок, выдавая их за
// UPC-E: на реальной накладной он «находил» код 04009795 шириной
// ноль пикселей на границе ячейки. Настоящий линейный код даже самый
// узкий занимает сотни пикселей — на замерах фикстур от 509 до 1118.
const minBarcodeWidth = 24

// filterOneD убирает результаты, которые не могут быть штрихкодом.
//
// Проверяется ширина: у линейного кода ZXing возвращает две точки —
// начала и конца строки — и у настоящего кода расстояние между ними
// большое. У вертикальной линии эти точки совпадают по X, то есть
// ширина нулевая.
func filterOneD(codes []Code) []Code {
	if len(codes) == 0 {
		return codes
	}

	kept := make([]Code, 0, len(codes))
	for _, c := range codes {
		if c.Kind != KindBarcode || codeWidth(c.Points) >= minBarcodeWidth {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// codeWidth — расстояние между крайними точками по горизонтали.
func codeWidth(points []image.Point) int {
	if len(points) < 2 {
		return 0
	}
	minX, maxX := points[0].X, points[0].X
	for _, p := range points[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
	}
	return maxX - minX
}

// convertResults превращает результаты ZXing в коды нашего формата.
func convertResults(results []*gozxing.Result, kind Kind) []Code {
	codes := make([]Code, 0, len(results))
	for _, res := range results {
		if res == nil {
			continue
		}
		text := res.GetText()
		if text == "" {
			continue
		}
		codes = append(codes, Code{
			Value:  text,
			Kind:   kind,
			Format: res.GetBarcodeFormat().String(),
			Points: resultPoints(res),
		})
	}
	return codes
}

func resultPoints(res *gozxing.Result) []image.Point {
	zxingPoints := res.GetResultPoints()
	if len(zxingPoints) == 0 {
		return nil
	}
	points := make([]image.Point, 0, len(zxingPoints))
	for _, p := range zxingPoints {
		if p == nil {
			continue
		}
		points = append(points, image.Pt(int(p.GetX()), int(p.GetY())))
	}
	return points
}