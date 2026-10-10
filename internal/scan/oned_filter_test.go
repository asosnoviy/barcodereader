package scan

import (
	"image"
	"image/color"
	"testing"

	"barcodereader/internal/testimages"
)

// Регрессия: ZXing при TRY_HARDER соглашается на вертикальные линии
// таблиц и рамок, выдавая их за штрихкод. На накладной ym-1.pdf он
// «находил» UPC-E 04009795 шириной ноль пикселей на границе ячейки.
func TestFilterOneDropsZeroWidthResults(t *testing.T) {
	verticalLine := Code{
		Value: "04009795", Kind: KindBarcode, Format: "UPC_E",
		Points: []image.Point{image.Pt(488, 355), image.Pt(488, 585)},
	}

	if got := filterOneD([]Code{verticalLine}); len(got) != 0 {
		t.Errorf("вертикальная линия шириной 0 не отфильтрована: %+v", got)
	}
}

func TestFilterOneDropsTooNarrowResults(t *testing.T) {
	narrow := Code{
		Value: "12345", Kind: KindBarcode, Format: "CODE_128",
		Points: []image.Point{image.Pt(10, 100), image.Pt(minBarcodeWidth-1, 100)},
	}
	if got := filterOneD([]Code{narrow}); len(got) != 0 {
		t.Errorf("слишком узкий код не отфильтрован: %+v", got)
	}
}

func TestFilterKeepsRealBarcode(t *testing.T) {
	// Настоящий EAN-13 на фикстуре занимает сотни пикселей.
	real := Code{
		Value: "4601234567893", Kind: KindBarcode, Format: "EAN_13",
		Points: []image.Point{image.Pt(100, 300), image.Pt(609, 300)},
	}
	got := filterOneD([]Code{real})
	if len(got) != 1 {
		t.Fatalf("настоящий штрихкод отфильтрован: %+v", got)
	}
}

// Фильтр не должен трогать двумерные коды: у Data Matrix и QR ширина
// по X вполне может оказаться небольшой.
func TestFilterKeepsMatrixCodes(t *testing.T) {
	matrix := Code{
		Value: "62822776321-1", Kind: KindMatrix, Format: "DATA_MATRIX",
		Points: []image.Point{image.Pt(15, 106), image.Pt(15, 15)},
	}
	got := filterOneD([]Code{matrix})
	if len(got) != 1 {
		t.Errorf("двумерный код не должен фильтроваться линейным правилом: %+v", got)
	}
}

func TestFilterOneDEmptyInput(t *testing.T) {
	if got := filterOneD(nil); got != nil {
		t.Errorf("filterOneD(nil) = %+v, ожидался nil", got)
	}
}

// Настоящий штрихкод с листа должен находиться и после включения фильтра:
// иначе «лечим» ложное срабатывание, ломая рабочий случай.
func TestRealBarcodeStillFoundAfterFilter(t *testing.T) {
	const value = "BARCODE-ONLY-VALUE"
	img := testimages.Barcode(value, testimages.Code128, 800, 180)

	codes := Scan(t.Context(), img, defaultOptions())

	found := false
	for _, c := range codes {
		if c.Kind == KindBarcode && c.Value == value {
			found = true
			if w := codeWidth(c.Points); w < minBarcodeWidth {
				t.Errorf("ширина %d меньше порога %d — фильтр проглотит настоящий код",
					w, minBarcodeWidth)
			}
		}
	}
	if !found {
		t.Fatalf("штрихкод %q не найден, получено %+v", value, codes)
	}
}

// По вертикальной линии таблицы ZXing не должен выдавать линейный код.
func TestVerticalLineNotReportedAsBarcode(t *testing.T) {
	// Лист с вертикальной линией, рамкой и текстом — тип страницы, на
	// котором ZXing срабатывал ложно.
	page := image.NewRGBA(image.Rect(0, 0, 900, 600))
	for y := range 600 {
		for x := range 900 {
			page.Set(x, y, color.Gray{Y: 255})
		}
	}
	for y := 100; y < 500; y++ {
		for x := 400; x < 404; x++ { // вертикальная линия в 4 пикселя
			page.Set(x, y, color.Gray{Y: 0})
		}
	}

	for _, c := range Scan(t.Context(), page, defaultOptions()) {
		if c.Kind == KindBarcode {
			t.Errorf("вертикальная линия выдана за штрихкод: %q %v", c.Value, c.Points)
		}
	}
}