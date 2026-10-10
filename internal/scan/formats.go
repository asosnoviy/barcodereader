package scan

import (
	"image"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/oned"
	"github.com/makiuchi-d/gozxing/oned/rss"
)

// oneDFactory создаёт ридер одного линейного формата.
//
// В gozxing нет MultiFormatReader: общий ридер собирается вручную перебором
// ридеров по форматам. Порядок имеет значение — сначала самые частые форматы.
type oneDFactory struct {
	name string
	new  func() gozxing.Reader
}

func oneDFactories() []oneDFactory {
	return []oneDFactory{
		{"upc/ean", func() gozxing.Reader { return oned.NewMultiFormatUPCEANReader(nil) }},
		{"code128", func() gozxing.Reader { return oned.NewCode128Reader() }},
		{"code39", func() gozxing.Reader { return oned.NewCode39Reader() }},
		{"code93", func() gozxing.Reader { return oned.NewCode93Reader() }},
		{"itf", func() gozxing.Reader { return oned.NewITFReader() }},
		{"codabar", func() gozxing.Reader { return oned.NewCodaBarReader() }},
		{"rss14", func() gozxing.Reader { return rss.NewRSS14Reader() }},
	}
}

// tryOneDReaders прогоняет изображение через ридеры форматов по очереди и
// возвращает все найденные коды (в пределах одной попытки — по одному на формат).
func tryOneDReaders(img image.Image, hints map[gozxing.DecodeHintType]any) []Code {
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil
	}

	var codes []Code
	for _, factory := range oneDFactories() {
		reader := factory.new()
		res, err := reader.Decode(bitmap, hints)
		reader.Reset()
		if err != nil || res == nil {
			continue
		}
		codes = append(codes, convertResults([]*gozxing.Result{res}, KindBarcode)...)
	}
	return codes
}