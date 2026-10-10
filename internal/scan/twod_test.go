package scan

import (
	"context"
	"image"
	"image/color"
	"testing"

	"barcodereader/internal/testimages"
)

const dmContent = "62863694784-1"

func TestScanFindsDataMatrix(t *testing.T) {
	img, err := testimages.DataMatrix(dmContent, 6, 4)
	if err != nil {
		t.Fatalf("генерация Data Matrix: %v", err)
	}

	codes := Scan(t.Context(), img, defaultOptions())
	if len(codes) == 0 {
		t.Fatal("Data Matrix не найден")
	}
	if codes[0].Value != dmContent {
		t.Errorf("value = %q, ожидалось %q", codes[0].Value, dmContent)
	}
	if codes[0].Kind != KindMatrix {
		t.Errorf("kind = %q, ожидалось %q", codes[0].Kind, KindMatrix)
	}
	if codes[0].Format != "DATA_MATRIX" {
		t.Errorf("format = %q, ожидалось \"DATA_MATRIX\"", codes[0].Format)
	}
}

func TestScanFindsInvertedBarcode(t *testing.T) {
	// Штрихкод, напечатанный белым по чёрному: обычные попытки его не видят.
	const value = "INVERTED-128"
	normal := testimages.Barcode(value, testimages.Code128, 800, 180)
	inverted := Invert(normal)

	codes := Scan(t.Context(), inverted, defaultOptions())

	found := false
	for _, c := range codes {
		if c.Kind == KindBarcode && c.Value == value {
			found = true
		}
	}
	if !found {
		t.Fatalf("инвертированный штрихкод не декодирован, найдено: %+v", codes)
	}
}

// paste помещает img на белый лист размера w×h со смещением (offX, offY).
func paste(img image.Image, w, h, offX, offY int) *image.RGBA {
	page := image.NewRGBA(image.Rect(0, 0, w, h))
	b := img.Bounds()
	for y := range b.Dy() {
		for x := range b.Dx() {
			page.Set(b.Min.X+x+offX, b.Min.Y+y+offY, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return page
}

// TestScanRegionsFindsCodeAwayFromCenter — главная особенность ZXing,
// из-за которой существует поиск по областям.
//
// WhiteRectangleDetector начинает осмотр от центра кадра. Символ, сдвинутый
// от центра, не находится ни при каком увеличении: поиск упирается в край
// страницы. Обрезка найденной области возвращает символ в центр кадра —
// и код читается.
func TestScanRegionsFindsCodeAwayFromCenter(t *testing.T) {
	const width, height = 1200, 900

	code, err := testimages.DataMatrix(dmContent, 8, 4)
	if err != nil {
		t.Fatalf("генерация Data Matrix: %v", err)
	}
	b := code.Bounds()

	spots := []struct {
		name     string
		offX     int
		offY     int
		inCenter bool
	}{
		{name: "центр", offX: (width - b.Dx()) / 2, offY: (height - b.Dy()) / 2, inCenter: true},
		{name: "левый-верх", offX: 40, offY: 40},
		{name: "правый-верх", offX: width - b.Dx() - 40, offY: 40},
		{name: "левый-низ", offX: 40, offY: height - b.Dy() - 40},
		{name: "правый-низ", offX: width - b.Dx() - 40, offY: height - b.Dy() - 40},
	}

	for _, spot := range spots {
		t.Run(spot.name, func(t *testing.T) {
			page := paste(code, width, height, spot.offX, spot.offY)

			direct := Scan(t.Context(), page, defaultOptions())
			if !spot.inCenter && len(direct) > 0 {
				t.Logf("код в %s читается и без поиска по областям", spot.name)
			}

			found := ScanRegions(t.Context(), page, defaultOptions(), 8)
			match := false
			for _, c := range found {
				if c.Value != dmContent {
					continue
				}
				match = true
				if c.Kind != KindMatrix || c.Format != "DATA_MATRIX" {
					t.Errorf("неверный тип: kind=%q format=%q", c.Kind, c.Format)
				}
				// Координаты должны быть в системе страницы, а не среза:
				// код стоит в углу со смещением 40, отрицательных значений
				// быть не может.
				for _, p := range c.Points {
					if p.X < 0 || p.Y < 0 {
						t.Fatalf("координаты не перенесены в систему страницы: %v", c.Points)
					}
				}
			}
			if !match {
				t.Fatalf("поиск по областям не нашёл Data Matrix (найдено %d: %+v)", len(found), found)
			}
		})
	}
}

func TestScanRegionsFindsSmallDataMatrix(t *testing.T) {
	code, err := testimages.DataMatrix(dmContent, 3, 4)
	if err != nil {
		t.Fatalf("генерация Data Matrix: %v", err)
	}
	b := code.Bounds()

	// Мелкий код в углу большого листа — как на настоящей этикетке.
	page := paste(code, 1600, 1200, 60, 1200-b.Dy()-60)

	if found := ScanRegions(t.Context(), page, defaultOptions(), 8); !hasValue(found, dmContent) {
		t.Fatalf("мелкий Data Matrix не найден, получено %d кодов", len(found))
	}
}

func TestScanRegionsOnBlankPage(t *testing.T) {
	blank := image.NewRGBA(image.Rect(0, 0, 1200, 900))

	if found := ScanRegions(t.Context(), blank, defaultOptions(), 8); len(found) != 0 {
		t.Errorf("на пустой странице ожидалось 0 кодов, получено %d", len(found))
	}
	if found := ScanRegions(t.Context(), nil, defaultOptions(), 8); len(found) != 0 {
		t.Errorf("для nil-изображения ожидалось 0 кодов, получено %d", len(found))
	}
}

func TestScanRegionsStopsOnCancelledContext(t *testing.T) {
	code, err := testimages.DataMatrix(dmContent, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	page := paste(code, 1200, 900, 40, 40)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if found := ScanRegions(ctx, page, defaultOptions(), 8); len(found) != 0 {
		t.Errorf("на отменённом контексте ожидалось 0 кодов, получено %d", len(found))
	}
}

func TestOtsuThresholdSplitsBimodalImage(t *testing.T) {
	// Половина тёмная, половина светлая: порог обязан лежать между ними.
	img := image.NewGray(image.Rect(0, 0, 100, 100))
	for y := range 100 {
		for x := range 100 {
			v := uint8(240)
			if x < 50 {
				v = 10
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}

	threshold := otsuThreshold(img)
	if threshold < 10 || threshold >= 240 {
		t.Errorf("порог %d вне диапазона между 10 и 240", threshold)
	}
}

func TestRegionRectStaysInsideImage(t *testing.T) {
	const width, height = 500, 400
	// Кандидат у самого края: поле не должно выводить срез за кадр.
	x0, y0, x1, y1 := expandRect(image.Rect(0, 0, 40, 40), 50, image.Rect(0, 0, width, height))
	if x0 < 0 || y0 < 0 || x1 > width || y1 > height {
		t.Errorf("срез вышел за кадр: %d,%d - %d,%d", x0, y0, x1, y1)
	}
	if x1 <= x0 || y1 <= y0 {
		t.Errorf("срез пуст: %d,%d - %d,%d", x0, y0, x1, y1)
	}
}

func hasValue(codes []Code, value string) bool {
	for _, c := range codes {
		if c.Value == value {
			return true
		}
	}
	return false
}