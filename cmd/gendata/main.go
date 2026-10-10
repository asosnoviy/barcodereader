// Command gendata генерирует PDF-фикстуры в каталоге testdata.
//
// Фикстуры коммитятся в репозиторий, чтобы тесты не зависели от генератора;
// команда нужна, когда требуется пересоздать их (например, после правки
// параметров сканирования).
//
//	go run ./cmd/gendata -out testdata
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"

	"github.com/go-pdf/fpdf"

	"barcodereader/internal/testimages"
)

// Значения кодов в фикстурах: тесты сверяются именно с ними.
const (
	valuePageOne     = "https://example.com/doc/page-1"
	valuePageThree   = "https://example.com/doc/page-3"
	valueDupSame     = "https://example.com/dup"
	valueMultiFirst  = "https://example.com/multi/first"
	valueMultiSecond = "https://example.com/multi/second"
	valueEAN         = "4601234567893"
	valueCode128     = "BARCODE-ONLY-VALUE"
)

func main() {
	outDir := flag.String("out", "testdata", "каталог для сгенерированных PDF")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("не удалось создать каталог %s: %v", *outDir, err)
	}

	fixtures := []struct {
		name string
		gen  func(*fpdf.Fpdf) error
	}{
		{"qr_vector.pdf", genVectorQR},
		{"qr_embedded_png.pdf", genEmbeddedPNG},
		{"qr_embedded_jpeg.pdf", genEmbeddedJPEG},
		{"scan_page.pdf", genScanPage},
		{"qr_multipage.pdf", genMultipage},
		{"qr_multi_on_page.pdf", genMultiOnPage},
		{"dup_values.pdf", genDuplicateValues},
		{"barcode_ean13.pdf", genQRAndEAN13},
		{"barcode_only.pdf", genBarcodeOnly},
		{"no_codes.pdf", genNoCodes},
	}

	for _, fixture := range fixtures {
		path := filepath.Join(*outDir, fixture.name)
		if err := writePDF(path, fixture.gen); err != nil {
			log.Fatalf("%s: %v", fixture.name, err)
		}
		log.Printf("создана фикстура %s", path)
	}

	if err := writeCorrupt(filepath.Join(*outDir, "corrupt.pdf")); err != nil {
		log.Fatalf("corrupt.pdf: %v", err)
	}
	log.Printf("создана фикстура %s", filepath.Join(*outDir, "corrupt.pdf"))
}

func newDoc() *fpdf.Fpdf {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(false, 15)
	return pdf
}

func writePDF(path string, gen func(*fpdf.Fpdf) error) error {
	pdf := newDoc()
	if err := gen(pdf); err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return fmt.Errorf("формирование PDF: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// drawVectorQR рисует QR модулями: в PDF не остаётся ни одного растрового
// объекта, поэтому находит код может только стадия рендера.
func drawVectorQR(pdf *fpdf.Fpdf, content string, x, y, size float64) {
	matrix := testimages.QRMatrix(content)
	rows := len(matrix)
	cols := len(matrix[0])
	// Тихая зона обязательна: без неё ZXing не находит код.
	const quiet = 4
	cell := size / float64(cols+2*quiet)

	pdf.SetFillColor(0, 0, 0)
	for row := range rows {
		for col := range cols {
			if !matrix[row][col] {
				continue
			}
			cx := x + float64(col+quiet)*cell
			cy := y + float64(row+quiet)*cell
			pdf.Rect(cx, cy, cell, cell, "F")
		}
	}
}

func registerPNG(pdf *fpdf.Fpdf, name string, img image.Image) error {
	data, err := testimages.PNG(img)
	if err != nil {
		return err
	}
	opts := fpdf.ImageOptions{ImageType: "png", ReadDpi: false}
	pdf.RegisterImageOptionsReader(name, opts, bytes.NewReader(data))
	return nil
}

func registerJPEG(pdf *fpdf.Fpdf, name string, img image.Image, quality int) error {
	data, err := testimages.JPEG(img, quality)
	if err != nil {
		return err
	}
	opts := fpdf.ImageOptions{ImageType: "jpg", ReadDpi: false}
	pdf.RegisterImageOptionsReader(name, opts, bytes.NewReader(data))
	return nil
}

// genVectorQR: QR нарисован прямоугольниками — работает только рендер.
func genVectorQR(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	pdf.SetFont("Arial", "", 14)
	pdf.CellFormat(0, 10, "Vector QR fixture", "", 1, "C", false, 0, "")
	drawVectorQR(pdf, valuePageOne, 20, 40, 80)
	return nil
}

// genEmbeddedPNG: QR вставлен картинкой — быстрый путь стадии A.
func genEmbeddedPNG(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	if err := registerPNG(pdf, "qr", testimages.QR(valuePageOne, 600)); err != nil {
		return err
	}
	pdf.Image("qr", 20, 30, 80, 80, false, "", 0, "")
	return nil
}

// genEmbeddedJPEG: QR в JPEG со сжатием — проверка устойчивости к артефактам.
func genEmbeddedJPEG(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	if err := registerJPEG(pdf, "qr", testimages.QR(valuePageOne, 600), 70); err != nil {
		return err
	}
	pdf.Image("qr", 20, 30, 80, 80, false, "", 0, "")
	return nil
}

// genScanPage: страница-скан с повёрнутым, размытым QR и шумом.
func genScanPage(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	degraded := testimages.Degrade(testimages.QR(valuePageOne, 900), testimages.DegradeOpts{
		RotateDeg: 8,
		Blur:      1,
		Noise:     14,
		JPEGQ:     65,
	})
	if err := registerJPEG(pdf, "scan", degraded, 60); err != nil {
		return err
	}
	pdf.Image("scan", 20, 30, 110, 110, false, "", 0, "")
	return nil
}

// genMultipage: QR на первой и третьей страницах с разными значениями.
func genMultipage(pdf *fpdf.Fpdf) error {
	if err := registerPNG(pdf, "qr", testimages.QR(valuePageOne, 600)); err != nil {
		return err
	}
	if err := registerPNG(pdf, "qr3", testimages.QR(valuePageThree, 600)); err != nil {
		return err
	}

	pdf.AddPage()
	pdf.Image("qr", 20, 30, 80, 80, false, "", 0, "")
	pdf.AddPage()
	pdf.Image("qr", 20, 30, 80, 80, false, "", 0, "")
	pdf.AddPage()
	pdf.Image("qr3", 20, 30, 80, 80, false, "", 0, "")
	return nil
}

// genMultiOnPage: два QR на одной странице — проверка DecodeMultiple.
func genMultiOnPage(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	if err := registerPNG(pdf, "qr1", testimages.QR(valueMultiFirst, 600)); err != nil {
		return err
	}
	if err := registerPNG(pdf, "qr2", testimages.QR(valueMultiSecond, 600)); err != nil {
		return err
	}
	pdf.Image("qr1", 15, 30, 80, 80, false, "", 0, "")
	pdf.Image("qr2", 110, 30, 80, 80, false, "", 0, "")
	return nil
}

// genDuplicateValues: одинаковый QR на стр. 1 и 3 плюс тот же текст в штрихкоде.
func genDuplicateValues(pdf *fpdf.Fpdf) error {
	if err := registerPNG(pdf, "dup", testimages.QR(valueDupSame, 600)); err != nil {
		return err
	}
	if err := registerPNG(pdf, "bc", testimages.Barcode(valueDupSame, testimages.Code128, 900, 150)); err != nil {
		return err
	}

	pdf.AddPage()
	pdf.Image("dup", 20, 20, 70, 70, false, "", 0, "")
	pdf.Image("bc", 15, 120, 150, 25, false, "", 0, "")
	pdf.AddPage()
	pdf.Image("dup", 20, 20, 70, 70, false, "", 0, "")
	pdf.AddPage()
	pdf.Image("dup", 20, 20, 70, 70, false, "", 0, "")
	return nil
}

// genQRAndEAN13: QR и линейный штрихкод рядом.
func genQRAndEAN13(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	if err := registerPNG(pdf, "qr", testimages.QR(valuePageOne, 600)); err != nil {
		return err
	}
	ean, err := testimages.EAN13(valueEAN, 3, 110)
	if err != nil {
		return err
	}
	if err := registerPNG(pdf, "ean", ean); err != nil {
		return err
	}
	pdf.Image("qr", 20, 25, 70, 70, false, "", 0, "")
	pdf.Image("ean", 15, 130, 90, 23, false, "", 0, "")
	return nil
}

// genBarcodeOnly: в документе есть только линейный штрихкод.
func genBarcodeOnly(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	if err := registerPNG(pdf, "bc", testimages.Barcode(valueCode128, testimages.Code128, 1200, 200)); err != nil {
		return err
	}
	pdf.Image("bc", 15, 40, 160, 27, false, "", 0, "")
	return nil
}

// genNoCodes: страница без кодов — ожидается 404 no_codes_found.
func genNoCodes(pdf *fpdf.Fpdf) error {
	pdf.AddPage()
	pdf.SetFont("Arial", "", 14)
	pdf.CellFormat(0, 10, "Документ без штрихкодов", "", 1, "C", false, 0, "")
	return nil
}

// writeCorrupt создаёт заведомо повреждённый PDF: заголовок есть, содержимое — мусор.
func writeCorrupt(path string) error {
	content := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0x00, 0xFF, 0x13, 0x37}, 512)...)
	return os.WriteFile(path, content, 0o644)
}