package pdfrender

import "testing"

// Регрессия: PointsPerPixel — это ПИКСЕЛИ на пункт, а не пункты на пиксель.
//
// PDFium отдаёт PointToPixelRatio в прямом смысле: при 400 dpi страница
// шириной 164 пункта рендерится в 912 пикселей, то есть отношение равно
// 5.56. Прежний код брал это значение как есть, и координаты в ответе
// API получались в 30 раз больше и уезжали за пределы страницы.
func TestPointsPerPixelIsInverseOfRatio(t *testing.T) {
	const dpi = 400
	const pointsPerInch = 72.0

	// Что отдаёт PDFium: пиксели на пункт.
	const pointToPixel = dpi / pointsPerInch // 5.55...

	page := Page{PointsPerPixel: 1 / pointToPixel}

	// 912 пикселей при 400 dpi — это ровно 164.16 пункта.
	const widthPx = 912.0
	if got := page.PixelsToPoints(widthPx); got != 164.16 {
		t.Errorf("PixelsToPoints(%.0f) = %v, ожидалось 164.16 пункта", widthPx, got)
	}

	// 100 пикселей — это 18 пунктов, а не 555.
	if got := page.PixelsToPoints(100); got != 18 {
		t.Errorf("PixelsToPoints(100) = %v, ожидалось 18 пунктов", got)
	}
}

func TestPixelsToPointsRoundsPerPixel(t *testing.T) {
	page := Page{PointsPerPixel: 72.0 / 200.0}

	if got := page.PixelsToPoints(200); got != 72.0 {
		t.Errorf("PixelsToPoints(200) = %v, ожидалось 72", got)
	}

	// Координата должна монотонно расти вместе с пикселем.
	prev := 0.0
	for _, px := range []float64{0, 1, 10, 100, 1000} {
		got := page.PixelsToPoints(px)
		if got < prev {
			t.Errorf("PixelsToPoints(%v) = %v меньше предыдущего %v", px, got, prev)
		}
		prev = got
	}

	// Без коэффициента координата возвращается как есть.
	plain := Page{}
	if got := plain.PixelsToPoints(42); got != 42 {
		t.Errorf("PixelsToPoints без коэффициента = %v, ожидалось 42", got)
	}
}