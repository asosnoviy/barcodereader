package scan

import (
	"context"
	"image"
	"testing"

	"barcodereader/internal/testimages"
)

const qrContent = "https://example.com/doc/42"

func defaultOptions() Options {
	return Options{
		MaxImageDim:     3000,
		OneDMaxWidth:    1600,
		IncludeBarcodes: true,
		ReturnAllQR:     true,
	}
}

func TestScanFindsQR(t *testing.T) {
	codes := Scan(t.Context(), testimages.QR(qrContent, 400), defaultOptions())

	if len(codes) == 0 {
		t.Fatal("QR не найден")
	}
	qr := codes[0]
	if qr.Value != qrContent {
		t.Errorf("value = %q, ожидалось %q", qr.Value, qrContent)
	}
	if qr.Kind != KindQR {
		t.Errorf("kind = %q, ожидалось %q", qr.Kind, KindQR)
	}
	if qr.Format != "QR_CODE" {
		t.Errorf("format = %q, ожидалось \"QR_CODE\"", qr.Format)
	}
	if len(qr.Points) == 0 {
		t.Error("ожидались координаты QR")
	}
}

func TestScanFindsMultipleQROnOneImage(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1200, 400))
	drawAt(canvas, testimages.QR("FIRST", 320), 40, 40)
	drawAt(canvas, testimages.QR("SECOND", 320), 720, 40)

	codes := Scan(t.Context(), canvas, defaultOptions())

	values := map[string]bool{}
	for _, c := range codes {
		if c.Kind == KindQR {
			values[c.Value] = true
		}
	}
	for _, want := range []string{"FIRST", "SECOND"} {
		if !values[want] {
			t.Errorf("QR %q не найден, найдено: %v", want, keys(values))
		}
	}
}

func TestScanFindsSingleQRWhenReturnAllDisabled(t *testing.T) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1200, 400))
	drawAt(canvas, testimages.QR("FIRST", 320), 40, 40)
	drawAt(canvas, testimages.QR("SECOND", 320), 720, 40)

	opts := defaultOptions()
	opts.ReturnAllQR = false

	codes := Scan(t.Context(), canvas, opts)

	qrCount := 0
	for _, c := range codes {
		if c.Kind == KindQR {
			qrCount++
		}
	}
	if qrCount == 0 {
		t.Fatal("QR не найден")
	}
}

func TestScanFindsBarcode(t *testing.T) {
	const value = "TEST-128-VALUE"
	bc := testimages.Barcode(value, testimages.Code128, 800, 200)

	codes := Scan(t.Context(), bc, defaultOptions())

	found := false
	for _, c := range codes {
		if c.Kind == KindBarcode {
			found = true
			if c.Value != value {
				t.Errorf("value = %q, ожидалось %q", c.Value, value)
			}
			if c.Format != "CODE_128" {
				t.Errorf("format = %q, ожидалось \"CODE_128\"", c.Format)
			}
			if len(c.Points) < 2 {
				t.Errorf("ожидались координаты концов штрихкода, получено %d точек", len(c.Points))
			}
		}
	}
	if !found {
		t.Fatalf("штрихкод не найден, найдено: %+v", codes)
	}
}

func TestScanFindsEAN13(t *testing.T) {
	const value = "4601234567893"
	img, err := testimages.EAN13(value, 3, 90)
	if err != nil {
		t.Fatalf("генерация EAN-13: %v", err)
	}

	codes := Scan(t.Context(), img, defaultOptions())

	found := false
	for _, c := range codes {
		if c.Kind == KindBarcode {
			found = true
			if c.Value != value {
				t.Errorf("value = %q, ожидалось %q", c.Value, value)
			}
			if c.Format != "EAN_13" {
				t.Errorf("format = %q, ожидалось \"EAN_13\"", c.Format)
			}
		}
	}
	if !found {
		t.Fatalf("EAN-13 не найден, найдено: %+v", codes)
	}
}

func TestScanSkipsBarcodesWhenDisabled(t *testing.T) {
	opts := defaultOptions()
	opts.IncludeBarcodes = false

	codes := Scan(t.Context(), testimages.Barcode("TEST-128", testimages.Code128, 800, 200), opts)

	for _, c := range codes {
		if c.Kind == KindBarcode {
			t.Fatalf("линейный штрихкод декодирован при IncludeBarcodes=false: %+v", c)
		}
	}
}

func TestScanFindsInvertedQR(t *testing.T) {
	inverted := Invert(testimages.QR(qrContent, 400))

	codes := Scan(t.Context(), inverted, defaultOptions())

	if len(codes) == 0 || codes[0].Value != qrContent {
		t.Fatalf("инвертированный QR не декодирован, найдено: %+v", codes)
	}
}

func TestScanOnDegradedQR(t *testing.T) {
	degraded := testimages.Degrade(testimages.QR(qrContent, 400), testimages.DegradeOpts{
		RotateDeg: 8,
		Blur:      1,
		Noise:     12,
		JPEGQ:     60,
	})

	codes := Scan(t.Context(), degraded, defaultOptions())

	if len(codes) == 0 || codes[0].Value != qrContent {
		t.Fatalf("QR после имитации скана не декодирован, найдено: %+v", codes)
	}
}

func TestScanOnBlankImage(t *testing.T) {
	blank := image.NewRGBA(image.Rect(0, 0, 400, 400))

	if codes := Scan(t.Context(), blank, defaultOptions()); len(codes) != 0 {
		t.Fatalf("на пустом изображении найдено %+v", codes)
	}
}

func TestScanNilImage(t *testing.T) {
	if codes := Scan(t.Context(), nil, defaultOptions()); codes != nil {
		t.Fatalf("для nil ожидался nil, получено %+v", codes)
	}
}

func TestScanRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if codes := Scan(ctx, testimages.QR(qrContent, 400), defaultOptions()); len(codes) != 0 {
		t.Fatalf("на отменённом контексте декодирование должно прекращаться, найдено %+v", codes)
	}
}

func drawAt(dst *image.RGBA, src image.Image, x, y int) {
	b := src.Bounds()
	for dy := range b.Dy() {
		for dx := range b.Dx() {
			dst.Set(b.Min.X+dx+x, b.Min.Y+dy+y, src.At(b.Min.X+dx, b.Min.Y+dy))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}