// Package scan декодирует QR-коды и линейные штрихкоды из растровых изображений
// на базе ZXing (порт gozxing).
package scan

import (
	"context"
	"image"

	"github.com/makiuchi-d/gozxing"
	zxingqr "github.com/makiuchi-d/gozxing/qrcode"
	zxingmultiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
)

// Kind различает QR-коды и линейные штрихкоды в едином массиве codes[].
type Kind string

const (
	KindQR      Kind = "qr"
	KindBarcode Kind = "barcode"
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
	// bitmap строит бинарное представление изображения.
	bitmap func(image.Image) (*gozxing.BinaryBitmap, error)
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

// hybridBitmap — стандартный путь: адаптивная бинаризация по яркости.
func hybridBitmap(img image.Image) (*gozxing.BinaryBitmap, error) {
	return gozxing.NewBinaryBitmapFromImage(img)
}

// globalBitmap — глобальная гистограмма; выручает на изображениях с сильной
// засветкой, где локальная адаптация даёт пустую картинку.
func globalBitmap(img image.Image) (*gozxing.BinaryBitmap, error) {
	source := gozxing.NewLuminanceSourceFromImage(img)
	return gozxing.NewBinaryBitmap(gozxing.NewGlobalHistgramBinarizer(source))
}

// invertedBitmap — светлый код на тёмном фоне: инвертируем яркость явно,
// без reliance на ALSO_INVERTED.
func invertedBitmap(img image.Image) (*gozxing.BinaryBitmap, error) {
	source := gozxing.NewInvertedLuminanceSource(gozxing.NewLuminanceSourceFromImage(img))
	return gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(source))
}

// qrAttempts — лестница попыток: от дешёвой и стандартной к более дорогим.
func qrAttempts() []qrAttempt {
	return []qrAttempt{
		{name: "hybrid", bitmap: hybridBitmap, hints: baseQRHints},
		{name: "hybrid+harder", bitmap: hybridBitmap, hints: tryHarderHints()},
		{name: "global", bitmap: globalBitmap, hints: baseQRHints},
		{name: "inverted", bitmap: invertedBitmap, hints: invertedHints()},
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
	if opts.IncludeBarcodes && ctx.Err() == nil {
		found = append(found, scanOneD(ctx, img, opts)...)
	}
	return found
}

// scanQR перебирает попытки, пока не найдёт QR-код(ы).
func scanQR(ctx context.Context, img image.Image, opts Options) []Code {
	scaled := Downscale(img, opts.MaxImageDim)

	for _, attempt := range qrAttempts() {
		if err := ctx.Err(); err != nil {
			return nil
		}
		bitmap, err := attempt.bitmap(scaled)
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
		if codes := tryOneDReaders(small, hintSet); len(codes) > 0 {
			return codes
		}
	}
	return nil
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