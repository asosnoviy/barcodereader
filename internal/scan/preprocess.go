package scan

import (
	"image"

	xdraw "golang.org/x/image/draw"
)

// Downscale уменьшает изображение так, чтобы ни одна сторона не превышала
// maxDim. Если лимит не задан или изображение уже достаточно мало, возвращается
// исходное изображение без копирования.
//
// Используется CatmullRom: он лучше билинейной интерполяции сохраняет резкие
// границы модулей QR-кода при сильном уменьшении.
func Downscale(img image.Image, maxDim int) image.Image {
	if img == nil || maxDim <= 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || (w <= maxDim && h <= maxDim) {
		return img
	}

	scale := float64(maxDim) / float64(maxInt(w, h))
	nw := scaleInt(w, scale)
	nh := scaleInt(h, scale)

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// DownscaleWidth уменьшает изображение по ширине, сохраняя пропорции. Если
// изображение уже у́же лимита, возвращается как есть.
//
// Билинейная интерполяция здесь уместна: картинка для линейных штрихкодов
// считается многократно (по строкам), и точность на уровне модулей QR тут не
// нужна, зато скорость важна.
func DownscaleWidth(img image.Image, maxWidth int) image.Image {
	if img == nil || maxWidth <= 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || w <= maxWidth {
		return img
	}

	nw := maxWidth
	nh := scaleInt(h, float64(nw)/float64(w))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// ToGray переводит изображение в 8-битную серую матрицу. ZXing всё равно
// считает яркость, но для серых данных это заметно дешевле.
func ToGray(img image.Image) *image.Gray {
	if img == nil {
		return nil
	}
	if g, ok := img.(*image.Gray); ok {
		return g
	}
	b := img.Bounds()
	dst := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			// RGBA() отдаёт премультиплицированные 16-битные каналы.
			// Пиксели с прозрачностью композитим по белому: иначе прозрачный
			// фон превратится в чёрный и код станет нечитаемым.
			r, g, bl, a := img.At(x, y).RGBA()
			white := 0xFFFF - a
			lum := (19595*(r+white) + 38470*(g+white) + 7471*(bl+white) + 1<<15) >> 16
			if lum > 0xFFFF {
				lum = 0xFFFF
			}
			dst.Pix[dst.PixOffset(x, y)] = uint8(lum >> 8)
		}
	}
	return dst
}

// Invert возвращает изображение с переставленными светлыми и тёмными тонами:
// светлый QR на тёмном фоне иначе не читается.
func Invert(img image.Image) *image.Gray {
	if img == nil {
		return nil
	}
	src := ToGray(img)
	dst := image.NewGray(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	for y := 0; y < src.Bounds().Dy(); y++ {
		for x := 0; x < src.Bounds().Dx(); x++ {
			s := src.Pix[src.PixOffset(x, y)]
			dst.Pix[dst.PixOffset(x, y)] = 255 - s
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

func scaleInt(value int, scale float64) int {
	scaled := int(float64(value)*scale + 0.5)
	if scaled < 1 {
		return 1
	}
	return scaled
}