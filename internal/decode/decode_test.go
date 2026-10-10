package decode

import (
	"context"
	"errors"
	"image"
	"image/draw"
	"io"
	"log/slog"
	"testing"

	"barcodereader/internal/apierrors"
	"barcodereader/internal/config"
	"barcodereader/internal/pdfrender"
	"barcodereader/internal/pdfembedded"
	"barcodereader/internal/scan"
	"barcodereader/internal/testimages"
)

// --- стабы стадий ---

type stubEmbedded struct {
	images []pdfembedded.Image
	err    error
	calls  int
}

func (s *stubEmbedded) Extract(_ context.Context, _ []byte, selection []int, _ int) ([]pdfembedded.Image, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if len(selection) == 0 {
		return s.images, nil
	}
	// Стадия A обязана уважать выборку страниц.
	var out []pdfembedded.Image
	for _, img := range s.images {
		for _, page := range selection {
			if img.PageNr == page {
				out = append(out, img)
				break
			}
		}
	}
	return out, nil
}

func (s *stubEmbedded) PageCount(context.Context, []byte) (int, error) { return 3, nil }

type stubRenderer struct {
	doc *stubDocument
	err error
	// opened считает вызовы Open: так проверяется, что стадия B не
	// запускается зря.
	opened int
}

func (s *stubRenderer) Open(context.Context, []byte) (Document, error) {
	s.opened++
	if s.err != nil {
		return nil, s.err
	}
	return s.doc, nil
}

type stubDocument struct {
	pages     int
	images    map[int]image.Image // 0-based индекс страницы
	closed    int
	rendered  int
	scaleToPt float64
}

func (d *stubDocument) PageCount() int { return d.pages }

func (d *stubDocument) PageSizeInPixels(_, dpi int) (int, int, error) {
	return 1654 * dpi / 200, 2339 * dpi / 200, nil
}

func (d *stubDocument) RenderPage(_ context.Context, index, _ int) (*pdfrender.Page, error) {
	d.rendered++
	img := d.images[index]
	if img == nil {
		img = image.NewRGBA(image.Rect(0, 0, 100, 100))
	}
	scale := d.scaleToPt
	if scale == 0 {
		scale = 72.0 / 200.0
	}
	return &pdfrender.Page{Image: img, DPI: 200, PointsPerPixel: scale}, nil
}

func (d *stubDocument) Close() error {
	d.closed++
	return nil
}

// --- помощники ---

func testConfig() config.Config {
	cfg := config.Default()
	cfg.QueueWait = 10_000_000 // 10ms
	cfg.MaxConcurrent = 4
	return cfg
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func embeddedQR(content string, page, obj int) pdfembedded.Image {
	return pdfembedded.Image{Img: testimages.QR(content, 300), PageNr: page, ObjNr: obj}
}

func defaultOptions() Options {
	return Options{IncludeBarcodes: true, ReturnAllQR: true, MaxPages: 10, DPI: 200}
}

// --- тесты ---

func TestStageAOnlyWhenBarcodesFound(t *testing.T) {
	embedded := &stubEmbedded{images: []pdfembedded.Image{embeddedQR("QR-1", 1, 3)}}
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.QR("QR-FROM-RENDER", 300)}}
	renderer := &stubRenderer{doc: doc}
	svc := New(testConfig(), renderer, embedded, quietLogger())

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if len(result.Codes) != 1 || result.Codes[0].Value != "QR-1" {
		t.Fatalf("коды = %+v, ожидался только QR-1", result.Codes)
	}
	if renderer.opened != 0 {
		t.Errorf("стадия B запущена %d раз, хотя штрихкод уже найден", renderer.opened)
	}
	if result.Value != "QR-1" {
		t.Errorf("value = %q", result.Value)
	}
}

func TestStageBRunsOnlyWhenForcedOrNothingFound(t *testing.T) {
	// Стадия A нашла QR. По умолчанию рендер пропускается — это и есть
	// быстрый путь. С ForceRender документ обходится целиком и находится
	// ещё и векторный штрихкод.
	embedded := &stubEmbedded{images: []pdfembedded.Image{embeddedQR("QR-1", 1, 1)}}
	doc := &stubDocument{
		pages: 1,
		images: map[int]image.Image{
			0: composeQRAndBarcode(),
		},
	}
	svc := New(testConfig(), &stubRenderer{doc: doc}, embedded, quietLogger())

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(result.Codes) != 1 {
		t.Fatalf("без force_render ожидался только QR из стадии A, получено %+v", result.Codes)
	}

	opts := defaultOptions()
	opts.ForceRender = true
	result, err = svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode с force_render: %v", err)
	}

	values := map[string]string{}
	for _, code := range result.Codes {
		values[code.Value] = code.Source
	}
	if values["QR-1"] != SourceEmbeddedImage {
		t.Errorf("QR-1 должен прийти из стадии A, получено %q", values["QR-1"])
	}
	if values["BARCODE-1"] != SourceRenderedPage {
		t.Errorf("BARCODE-1 должен прийти из рендера, получено %q", values["BARCODE-1"])
	}
}

func TestStageBSkippedWhenBarcodesDisabled(t *testing.T) {
	embedded := &stubEmbedded{images: []pdfembedded.Image{embeddedQR("QR-1", 1, 1)}}
	renderer := &stubRenderer{doc: &stubDocument{pages: 1}}
	svc := New(testConfig(), renderer, embedded, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if renderer.opened != 0 {
		t.Errorf("рендер выполнен при IncludeBarcodes=false")
	}
}

func TestStageASoftFailureFallsBackToRender(t *testing.T) {
	embedded := &stubEmbedded{err: errors.New("какая-то внутренняя ошибка pdfcpu")}
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.QR("FROM-RENDER", 300)}}
	renderer := &stubRenderer{doc: doc}
	svc := New(testConfig(), renderer, embedded, quietLogger())

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err != nil {
		t.Fatalf("мягкая ошибка стадии A не должна пробрасываться: %v", err)
	}
	if result.Value != "FROM-RENDER" {
		t.Errorf("value = %q, ожидалось FROM-RENDER", result.Value)
	}
}

func TestStageAHardFailureIsReturned(t *testing.T) {
	embedded := &stubEmbedded{err: apierrors.PDFEncrypted(errors.New("encrypted"))}
	renderer := &stubRenderer{doc: &stubDocument{pages: 1}}
	svc := New(testConfig(), renderer, embedded, quietLogger())

	_, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	var apiErr *apierrors.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ожидалась apierrors.Error, получено %v", err)
	}
	if apiErr.Code != "pdf_encrypted" {
		t.Errorf("code = %q, ожидалось pdf_encrypted", apiErr.Code)
	}
	if renderer.opened != 0 {
		t.Error("при нечитаемом документе рендер запускаться не должен")
	}
}

func TestDeduplicatesEqualValuesAcrossPages(t *testing.T) {
	// Один и тот же QR на стр. 1 и стр. 3 — остаётся наименьшая страница.
	doc := &stubDocument{
		pages: 3,
		images: map[int]image.Image{
			0: testimages.QR("SAME", 300),
			2: testimages.QR("SAME", 300),
		},
	}
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if len(result.Codes) != 1 {
		t.Fatalf("после свёртки ожидалась 1 запись, получено %d: %+v", len(result.Codes), result.Codes)
	}
	if result.Codes[0].Page != 1 {
		t.Errorf("page = %d, ожидалась 1", result.Codes[0].Page)
	}
	if result.Meta.CodesTotal != 2 || result.Meta.CodesUnique != 1 || result.Meta.DuplicatesCollapsed != 1 {
		t.Errorf("meta = %+v, ожидалось total=2 unique=1 collapsed=1", result.Meta)
	}
}

func TestDeduplicatesQRAndBarcodeWithSameValue(t *testing.T) {
	// Одинаковый текст в QR (стадия A) и в штрихкоде (рендер).
	// ForceRender нужен, чтобы стадия B отработала несмотря на находку в A.
	embedded := &stubEmbedded{images: []pdfembedded.Image{embeddedQR("12345", 1, 1)}}
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.Barcode("12345", testimages.Code128, 600, 120)}}
	svc := New(testConfig(), &stubRenderer{doc: doc}, embedded, quietLogger())

	opts := defaultOptions()
	opts.ForceRender = true

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if result.Meta.CodesTotal != 2 {
		t.Fatalf("codes_total = %d, ожидалось 2 (до свёртки)", result.Meta.CodesTotal)
	}
	if len(result.Codes) != 1 {
		t.Fatalf("ожидалась 1 запись после свёртки, получено %d: %+v", len(result.Codes), result.Codes)
	}
	// Остаётся формат первого найденного — стадия A идёт раньше рендера.
	if result.Codes[0].Format != "QR_CODE" {
		t.Errorf("format = %q, ожидался QR_CODE (первое найденное)", result.Codes[0].Format)
	}
	if result.Meta.DuplicatesCollapsed != 1 {
		t.Errorf("duplicates_collapsed = %d, ожидалось 1", result.Meta.DuplicatesCollapsed)
	}
}

func TestPrimaryPrefersQROverBarcode(t *testing.T) {
	embedded := &stubEmbedded{images: []pdfembedded.Image{embeddedQR("QR-VALUE", 1, 1)}}
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.Barcode("BARCODE-VALUE", testimages.Code128, 600, 120)}}
	svc := New(testConfig(), &stubRenderer{doc: doc}, embedded, quietLogger())

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if result.Value != "QR-VALUE" {
		t.Errorf("value = %q, ожидалось QR-VALUE", result.Value)
	}
}

func TestPrimaryFallsBackToBarcode(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.Barcode("ONLY-BARCODE", testimages.Code128, 600, 120)}}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if result.Value != "ONLY-BARCODE" {
		t.Errorf("value = %q", result.Value)
	}
	if result.Codes[0].Kind != "barcode" {
		t.Errorf("kind = %q, ожидался barcode", result.Codes[0].Kind)
	}
}

func TestNoCodesFoundError(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	doc := &stubDocument{pages: 2}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	_, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), defaultOptions())
	var apiErr *apierrors.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ожидалась apierrors.Error, получено %v", err)
	}
	if apiErr.Code != "no_codes_found" {
		t.Errorf("code = %q", apiErr.Code)
	}
	if apiErr.HTTPStatus != 404 {
		t.Errorf("HTTPStatus = %d, ожидалось 404", apiErr.HTTPStatus)
	}
}

func TestRespectsPageSelection(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	doc := &stubDocument{
		pages: 4,
		images: map[int]image.Image{
			1: testimages.QR("PAGE-TWO", 300),
			3: testimages.QR("PAGE-FOUR", 300),
		},
	}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false
	opts.Pages = []int{2}

	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(result.Codes) != 1 || result.Codes[0].Value != "PAGE-TWO" {
		t.Fatalf("коды = %+v, ожидался только QR со стр. 2", result.Codes)
	}
	if doc.rendered != 1 {
		t.Errorf("отрендерено %d страниц, ожидалась 1", doc.rendered)
	}
}

func TestRespectsMaxPages(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	// Без повторного прохода по DPI каждый page рендерится ровно один раз.
	cfg.RetryHigherDPI = false
	cfg.RegionSearch = false
	doc := &stubDocument{pages: 10}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false
	opts.MaxPages = 3

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err == nil {
		t.Fatal("ожидалась ошибка no_codes_found")
	}
	if doc.rendered != 3 {
		t.Errorf("отрендерено %d страниц, ожидалось 3", doc.rendered)
	}
}

func TestDPILadderRetriesWhenNothingFound(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	cfg.RetryHigherDPI = true
	cfg.RegionSearch = false
	cfg.RenderDPI, cfg.RenderDPIMax = 200, 400
	doc := &stubDocument{pages: 1}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false
	opts.DPI = 200

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err == nil {
		t.Fatal("ожидалась ошибка no_codes_found")
	}
	if doc.rendered != 2 {
		t.Errorf("страниц отрендерено %d, ожидалось 2 (лестница DPI)", doc.rendered)
	}
}

// Страница может быть отрисована несколько раз: лестница DPI, затем поиск по
// областям. meta.pages_scanned обязан оставаться числом страниц, а не числом
// рендеров, иначе в ответе появляется pages_scanned > pages_total.
func TestPagesScannedCountsPagesNotRenders(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	cfg.RetryHigherDPI = true
	cfg.RegionSearch = true
	cfg.RenderDPI, cfg.RenderDPIMax = 200, 400
	// Единственная страница без кодов: её трижды отрендерят (200 dpi, 400 dpi,
	// поиск по областям), но проскана была одна страница.
	doc := &stubDocument{pages: 1}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false
	opts.DPI = 200

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err == nil {
		t.Fatal("ожидалась ошибка no_codes_found")
	}
	if doc.rendered != 3 {
		t.Errorf("рендеров %d, ожидалось 3 (лестница DPI + поиск по областям)", doc.rendered)
	}

	// Проверяем сам счётчик через успешный ответ: код найден, но рендеров было
	// больше, чем страниц.
	found := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.QR("ЕСТЬ", 300)}}
	svc = New(cfg, &stubRenderer{doc: found}, nil, quietLogger())

	res, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if res.Meta.PagesScanned != 1 {
		t.Errorf("pages_scanned = %d, ожидалась 1", res.Meta.PagesScanned)
	}
	if res.Meta.PagesScanned > res.Meta.PagesTotal {
		t.Errorf("pages_scanned (%d) больше pages_total (%d)",
			res.Meta.PagesScanned, res.Meta.PagesTotal)
	}
}

// Страница с уже найденным QR не должна обходить��я поиском по областям:
// двумерный код на ней уже есть, а лишний рендер ничего не добавит.
func TestRegionSearchSkipsPagesWithMatrix(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	cfg.RetryHigherDPI = false
	cfg.RegionSearch = true
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.QR("НАЙДЕНО", 300)}}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if doc.rendered != 1 {
		t.Errorf("страниц отрендерено %d, ожидался 1: QR найден, повторный обход не нужен", doc.rendered)
	}

	// Пустая страница: обычное сканирование не даёт ничего, значит
	// добавляется ещё один рендер под поиск по областям.
	empty := &stubDocument{pages: 1}
	svc = New(cfg, &stubRenderer{doc: empty}, nil, quietLogger())
	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err == nil {
		t.Fatal("ожидалась ошибка no_codes_found")
	}
	if empty.rendered != 2 {
		t.Errorf("страниц отрендерено %d, ожидалось 2 (обычный проход + поиск по областям)", empty.rendered)
	}
}

// Регрессия: Data Matrix не должен теряться рядом с линейным штрихкодом.
//
// Детектор Data Matrix ищет символ от центра кадра, поэтому на этикетке он
// часто не находится обычным проходом. Но там же лежит штрихкод, который
// находится легко, и прежнее условие «искать по областям только если не
// найдено вообще ничего» молча проглатывало двумерный код.
func TestRegionSearchFindsMatrixNextToBarcode(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	cfg.RetryHigherDPI = false
	cfg.RegionSearch = true

	// Страница: штрихкод внизу по центру (находится легко) и Data Matrix
	// в углу (находится только поиском по областям).
	matrix, err := testimages.DataMatrix("62822776321-1", 6, 4)
	if err != nil {
		t.Fatalf("генерация Data Matrix: %v", err)
	}
	barcode := testimages.Barcode("04009795", testimages.Code128, 500, 120)

	page := image.NewRGBA(image.Rect(0, 0, 900, 600))
	draw.Draw(page, page.Bounds(), image.White, image.Point{}, draw.Src)
	pasteAt(page, matrix, 40, 40)
	pasteAt(page, barcode, 200, 430)

	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: page}}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	res, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	values := make(map[string]string, len(res.Codes))
	for _, c := range res.Codes {
		values[c.Value] = c.Kind
	}
	if kind, ok := values["62822776321-1"]; !ok {
		t.Errorf("Data Matrix не найден рядом со штрихкодом, получено: %+v", res.Codes)
	} else if kind != string(scan.KindMatrix) {
		t.Errorf("kind Data Matrix = %q, ожидался %q", kind, scan.KindMatrix)
	}
	if _, ok := values["04009795"]; !ok {
		t.Errorf("штрихкод потерян, получено: %+v", res.Codes)
	}
}

func pasteAt(dst *image.RGBA, src image.Image, offX, offY int) {
	b := src.Bounds()
	for y := range b.Dy() {
		for x := range b.Dx() {
			dst.Set(b.Min.X+x+offX, b.Min.Y+y+offY, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
}

func TestRegionSearchDisabledInConfig(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	cfg.RetryHigherDPI = false
	cfg.RegionSearch = false
	empty := &stubDocument{pages: 1}
	svc := New(cfg, &stubRenderer{doc: empty}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false

	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err == nil {
		t.Fatal("ожидалась ошибка no_codes_found")
	}
	if empty.rendered != 1 {
		t.Errorf("страниц отрендерено %d, ожидался 1: поиск по областям отключён", empty.rendered)
	}
}

func TestClosesDocument(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	doc := &stubDocument{pages: 1, images: map[int]image.Image{0: testimages.QR("X", 300)}}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false
	if _, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if doc.closed != 1 {
		t.Errorf("документ закрыт %d раз, ожидался 1", doc.closed)
	}
}

func TestCancelledContext(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	doc := &stubDocument{pages: 1}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := svc.Decode(ctx, []byte("%PDF-1.7"), defaultOptions())
	var apiErr *apierrors.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "timeout" {
		t.Fatalf("ожидался timeout, получено %v", err)
	}
}

func TestRenderedCoordinatesConvertedToPoints(t *testing.T) {
	cfg := testConfig()
	cfg.EnableEmbeddedStage = false
	doc := &stubDocument{
		pages:     1,
		images:    map[int]image.Image{0: testimages.QR("COORD", 400)},
		scaleToPt: 72.0 / 200.0,
	}
	svc := New(cfg, &stubRenderer{doc: doc}, nil, quietLogger())

	opts := defaultOptions()
	opts.IncludeBarcodes = false
	result, err := svc.Decode(t.Context(), []byte("%PDF-1.7"), opts)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	box := result.Codes[0].BBox
	if box == nil {
		t.Fatal("bbox не вычислен")
	}
	// A4 — 595×842 пункта; код не может вылезать за страницу.
	if box.X < 0 || box.Y < 0 || box.X+box.Width > 595 || box.Y+box.Height > 842 {
		t.Errorf("bbox %+v выходит за пределы страницы A4", box)
	}
}

// composeQRAndBarcode склеивает QR и штрихкод на одном холсте.
func composeQRAndBarcode() image.Image {
	canvas := image.NewRGBA(image.Rect(0, 0, 800, 500))
	qr := testimages.QR("QR-1", 300)
	drawImage(canvas, qr, 40, 40)
	drawImage(canvas, testimages.Barcode("BARCODE-1", testimages.Code128, 500, 100), 40, 380)
	return canvas
}

func drawImage(dst *image.RGBA, src image.Image, x, y int) {
	b := src.Bounds()
	for dy := range b.Dy() {
		for dx := range b.Dx() {
			dst.Set(b.Min.X+dx+x, b.Min.Y+dy+y, src.At(b.Min.X+dx, b.Min.Y+dy))
		}
	}
}