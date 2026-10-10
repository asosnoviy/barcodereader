// Package testimages генерирует растровые заготовки для тестов и фикстур:
// QR-коды, линейные штрихкоды и их «испорченные» версии, имитирующие скан.
//
// Пакет нужен только dev-инструментам (тесты и cmd/gendata) и не попадает
// в сервисный бинарник: его никто из cmd/barcodereader не импортирует.
package testimages

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/datamatrix"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/skip2/go-qrcode"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
)

// Symbology — поддерживаемые линейные форматы генератора.
type Symbology string

const Code128 Symbology = "code128"

// QR возвращает изображение QR-кода размером size×size пикселей.
func QR(content string, size int) image.Image {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		// Некорректное содержимое — ошибка разработчика фикстуры, падать рано.
		panic("testimages: не удалось построить QR: " + err.Error())
	}
	return code.Image(size)
}

// QRMatrix возвращает матрицу модулей QR: true — тёмный модуль.
// Нужна для рисования векторного QR в PDF.
func QRMatrix(content string) [][]bool {
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		panic("testimages: не удалось построить QR: " + err.Error())
	}
	return code.Bitmap()
}

// Barcode возвращает линейный штрихкод заданного формата.
//
// Поддерживается только Code128: генератор boombuler масштабирует модули с
// округлением, из-за чего EAN-13 в его исполнении ZXing не принимает.
// Для EAN-13 используйте EAN13.
func Barcode(content string, symb Symbology, width, height int) image.Image {
	if symb != Code128 {
		panic("testimages: этот формат бери через специализированный генератор, symbology=" + string(symb))
	}
	bc, err := code128.Encode(content)
	if err != nil {
		panic("testimages: не удалось построить штрихкод: " + err.Error())
	}
	scaled, err := barcode.Scale(bc, width, height)
	if err != nil {
		panic("testimages: не удалось масштабировать штрихкод: " + err.Error())
	}
	return scaled
}

// EAN13 рисует EAN-13 штатным writer'ом ZXing: его выход заведомо читается
// тем же декодером, поэтому фикстура не зависит от особенностей чужого
// рендеринга. scale задаёт ширину модуля в пикселях.
func EAN13(content string, scale, height int) (image.Image, error) {
	matrix, err := oned.NewEAN13Writer().Encode(content, gozxing.BarcodeFormat_EAN_13, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	// +2 модуля тишины с каждой стороны: без них ZXing отвергает код.
	const quiet = 10
	module := maxInt(1, scale)
	width := (matrix.GetWidth() + 2*quiet) * module

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			col := x/module - quiet
			black := col >= 0 && col < matrix.GetWidth() && matrix.Get(col, 0)
			if black {
				dst.Set(x, y, color.Black)
			} else {
				dst.Set(x, y, color.White)
			}
		}
	}
	return dst, nil
}

// DataMatrix рисует код Data Matrix штатным writer'ом ZXing: его выход
// заведомо читается тем же декодером, поэтому фикстура не зависит от
// особенностей чужого рендеринга.
func DataMatrix(content string, scale, quiet int) (image.Image, error) {
	matrix, err := datamatrix.NewDataMatrixWriter().Encode(content, gozxing.BarcodeFormat_DATA_MATRIX, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	return fromBitMatrix(matrix, scale, quiet), nil
}

// fromBitMatrix превращает битовую матрицу ZXing в изображение с тихой зоной.
// Работает и с однострочными матрицами линейных кодов, и с квадратными
// матрицами Data Matrix.
func fromBitMatrix(matrix *gozxing.BitMatrix, scale, quiet int) image.Image {
	if scale < 1 {
		scale = 1
	}
	if quiet < 0 {
		quiet = 0
	}

	modulesX := matrix.GetWidth()
	modulesY := matrix.GetHeight()
	width := (modulesX + 2*quiet) * scale
	height := (modulesY + 2*quiet) * scale
	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := range height {
		for x := range width {
			col := x/scale - quiet
			row := y/scale - quiet
			black := col >= 0 && col < modulesX && row >= 0 && row < modulesY && matrix.Get(col, row)
			if black {
				dst.Set(x, y, color.Black)
			} else {
				dst.Set(x, y, color.White)
			}
		}
	}
	return dst
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PNG кодирует изображение в PNG.
func PNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// JPEG кодирует изображение в JPEG с заданным качеством (1..100).
func JPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DegradeOpts описывает имитацию скана: поворот, размытие, шум и потери JPEG.
type DegradeOpts struct {
	RotateDeg float64
	Blur      int
	Noise     int
	JPEGQ     int
}

// Degrade превращает чистое изображение в «скан»: слегка поворачивает,
// размывает, добавляет шум и пережимает в JPEG.
func Degrade(img image.Image, opts DegradeOpts) image.Image {
	out := img
	if opts.RotateDeg != 0 {
		out = Rotate(out, opts.RotateDeg)
	}
	if opts.Blur > 0 {
		out = Blur(out, opts.Blur)
	}
	if opts.Noise > 0 {
		out = AddNoise(out, opts.Noise)
	}
	if opts.JPEGQ > 0 {
		encoded, err := JPEG(out, opts.JPEGQ)
		if err == nil {
			decoded, err := jpeg.Decode(bytes.NewReader(encoded))
			if err == nil {
				out = decoded
			}
		}
	}
	return out
}

// Rotate поворачивает изображение на заданный угол по часовой стрелке.
// Размер холста сохраняется: углы заполняются белым.
func Rotate(img image.Image, degrees float64) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))

	rad := degrees * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	cx, cy := float64(w)/2, float64(h)/2

	for y := range h {
		for x := range w {
			// Обратное преобразование: из координаты результата в исходную.
			dx, dy := float64(x)-cx+0.5, float64(y)-cy+0.5
			srcX := cos*dx + sin*dy + cx - 0.5
			srcY := -sin*dx + cos*dy + cy - 0.5
			dst.Set(x, y, bilinear(img, srcX, srcY))
		}
	}
	return dst
}

// bilinear интерполирует цвет в точке (x, y) исходного изображения.
// Каналы нормализуются делением на 257, а не на 65535: uint8 от диапазона
// 0..1 дал бы 0 или 1 вместо 0 или 255.
func bilinear(img image.Image, x, y float64) color.Color {
	b := img.Bounds()
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)

	var r, g, bl, a float64
	for dy := range 2 {
		for dx := range 2 {
			wx, wy := 1.0-fx, 1.0-fy
			if dx == 1 {
				wx = fx
			}
			if dy == 1 {
				wy = fy
			}
			px, py := x0+dx, y0+dy
			if px < b.Min.X || px >= b.Max.X || py < b.Min.Y || py >= b.Max.Y {
				continue
			}
			pr, pg, pb, pa := img.At(px, py).RGBA()
			r += float64(pr) * wx * wy
			g += float64(pg) * wx * wy
			bl += float64(pb) * wx * wy
			a += float64(pa) * wx * wy
		}
	}
	// Компоненты color.RGBA ожидаются в премультиплицированном виде.
	return color.RGBA{
		uint8(clampFloat(r / 257.0)),
		uint8(clampFloat(g / 257.0)),
		uint8(clampFloat(bl / 257.0)),
		uint8(clampFloat(a / 257.0)),
	}
}

func clampFloat(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	default:
		return v
	}
}

// Blur применяет box-blur с радиусом radius (в пикселях).
func Blur(img image.Image, radius int) image.Image {
	if radius <= 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))

	for y := range h {
		for x := range w {
			var r, g, bl, n float64
			for dy := -radius; dy <= radius; dy++ {
				for dx := -radius; dx <= radius; dx++ {
					px, py := x+dx, y+dy
					if px < 0 || px >= w || py < 0 || py >= h {
						continue
					}
					pr, pg, pb, _ := img.At(px, py).RGBA()
					r += float64(pr)
					g += float64(pg)
					bl += float64(pb)
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				uint8(r / n / 257), uint8(g / n / 257), uint8(bl / n / 257), 255,
			})
		}
	}
	return dst
}

// AddNoise добавляет равномерный шум амплитудой amplitude (0..255).
func AddNoise(img image.Image, amplitude int) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	seed := uint64(0x2545F4914F6CDD1D)

	for y := range b.Dy() {
		for x := range b.Dx() {
			// xorshift64* — детерминированный шум: фикстуры должны быть
			// воспроизводимы побайтово между запусками.
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			delta := int(seed%uint64(2*amplitude+1)) - amplitude

			r, g, bl, _ := img.At(x, y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				clamp8(int(r/257) + delta),
				clamp8(int(g/257) + delta),
				clamp8(int(bl/257) + delta),
				255,
			})
		}
	}
	return dst
}

func clamp8(v int) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	default:
		return uint8(v)
	}
}