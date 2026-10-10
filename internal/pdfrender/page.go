package pdfrender

import "math"

// minDPI — нижняя граница рендера. Ниже PDFium начинает терять детали QR,
// а ZXing плохо работает с мелким растром.
const minDPI = 72

// FitDPI возвращает DPI, при котором страница не превысит бюджет пикселей.
//
// widthPx и heightPx — размер страницы при wantDPI. Если он укладывается в
// бюджет, возвращается wantDPI без изменений; иначе DPI пропорционально
// уменьшается (площадь растёт как квадрат DPI), но не ниже minDPI.
func FitDPI(widthPx, heightPx, wantDPI int, maxPixels int64) int {
	if wantDPI <= 0 || widthPx <= 0 || heightPx <= 0 {
		return wantDPI
	}
	if maxPixels <= 0 {
		return max(wantDPI, minDPI)
	}

	pixels := int64(widthPx) * int64(heightPx)
	if pixels <= maxPixels {
		return wantDPI
	}

	scale := math.Sqrt(float64(maxPixels) / float64(pixels))
	fitted := int(float64(wantDPI) * scale)
	return max(fitted, minDPI)
}