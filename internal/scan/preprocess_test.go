package scan

import (
	"image"
	"image/color"
	"testing"

	"barcodereader/internal/testimages"
)

func TestDownscaleRespectsMaxDim(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 500))

	got := Downscale(img, 250)
	b := got.Bounds()
	if b.Dx() != 250 || b.Dy() != 125 {
		t.Fatalf("получили %dx%d, ожидалось 250x125", b.Dx(), b.Dy())
	}
}

func TestDownscaleKeepsSmallImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 80))

	got := Downscale(img, 3000)
	if got != image.Image(img) {
		t.Error("изображение меньше лимита не должно копироваться")
	}
}

func TestDownscaleHandlesNonPositiveLimit(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 80))

	if got := Downscale(img, 0); got != image.Image(img) {
		t.Error("нулевой лимит должен оставлять изображение как есть")
	}
	if Downscale(nil, 100) != nil {
		t.Error("nil должен возвращаться как nil")
	}
}

func TestDownscaleWidth(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))

	got := DownscaleWidth(img, 200)
	b := got.Bounds()
	if b.Dx() != 200 || b.Dy() != 100 {
		t.Fatalf("получили %dx%d, ожидалось 200x100", b.Dx(), b.Dy())
	}
}

func TestDownscaleWidthKeepsNarrowImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 400))

	if got := DownscaleWidth(img, 1600); got != image.Image(img) {
		t.Error("узкое изображение не должно уменьшаться")
	}
}

func TestToGrayKeepsGrayUntouched(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 4, 4))

	if got := ToGray(img); got != img {
		t.Error("изображение Gray должно возвращаться без копирования")
	}
	if ToGray(nil) != nil {
		t.Error("nil должен возвращаться как nil")
	}
}

func TestToGrayConvertsRGB(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, image.White)
	img.Set(1, 0, image.Black)
	img.Set(0, 1, image.White)
	img.Set(1, 1, image.Black)

	got := ToGray(img)
	if v := got.GrayAt(0, 0).Y; v != 255 {
		t.Errorf("белый пиксель = %d, ожидалось 255", v)
	}
	if v := got.GrayAt(1, 0).Y; v != 0 {
		t.Errorf("чёрный пиксель = %d, ожидалось 0", v)
	}
}

func TestInvertSwapsExtremes(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	img.SetGray(0, 0, color.Gray{Y: 255})
	img.SetGray(1, 0, color.Gray{Y: 0})

	got := Invert(img)
	if v := got.GrayAt(0, 0).Y; v != 0 {
		t.Errorf("инверсия белого = %d, ожидалось 0", v)
	}
	if v := got.GrayAt(1, 0).Y; v != 255 {
		t.Errorf("инверсия чёрного = %d, ожидалось 255", v)
	}
}

func TestDownscalePreservesDecodeableQR(t *testing.T) {
	const content = "https://example.com/scale-test"
	original := testimages.QR(content, 600)
	scaled := Downscale(original, 220)

	codes := Scan(t.Context(), scaled, Options{MaxImageDim: 400, ReturnAllQR: true})
	if len(codes) == 0 {
		t.Fatal("QR не декодировался после уменьшения")
	}
	if codes[0].Value != content {
		t.Errorf("value = %q, ожидалось %q", codes[0].Value, content)
	}
}