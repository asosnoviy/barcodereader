package scan

import (
	"image"

	"github.com/makiuchi-d/gozxing"
)

// luminance — источник яркости, посчитанный один раз на изображение.
//
// gozxing пересчитывает яркость при каждом NewBinaryBitmapFromImage, а
// попыток на страницу одиннадцать: четыре для QR, две для Data Matrix и
// четыре прохода линейных кодов. На странице 1654×2339 это лишние
// миллионы чтений пикселей на каждый вызов, хотя картинка одна и та же.
//
// Делить можно только источник: после создания он только читается.
// Бинарный представитель делить нельзя — у каждого бинаризатора свой кеш
// матрицы, и общий кеш смешал бы результаты разных попыток.
type luminance struct {
	img image.Image
	src gozxing.LuminanceSource
}

func newLuminance(img image.Image) *luminance {
	return &luminance{img: img}
}

// source считает яркость при первом обращении и дальше отдаёт тот же буфер.
func (l *luminance) source() gozxing.LuminanceSource {
	if l.src == nil {
		l.src = gozxing.NewLuminanceSourceFromImage(l.img)
	}
	return l.src
}

// hybrid — стандартный путь: адаптивная бинаризация по яркости.
func (l *luminance) hybrid() (*gozxing.BinaryBitmap, error) {
	return gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(l.source()))
}

// global — глобальная гистограмма; выручает на изображениях с сильной
// засветкой, где локальная адаптация даёт пустую картинку.
func (l *luminance) global() (*gozxing.BinaryBitmap, error) {
	return gozxing.NewBinaryBitmap(gozxing.NewGlobalHistgramBinarizer(l.source()))
}

// inverted — светлый код на тёмном фоне: инвертируем яркость явно,
// без reliance на ALSO_INVERTED.
func (l *luminance) inverted() (*gozxing.BinaryBitmap, error) {
	src := gozxing.NewInvertedLuminanceSource(l.source())
	return gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(src))
}