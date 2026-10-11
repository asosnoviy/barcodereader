package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"barcodereader/internal/config"
	"barcodereader/internal/decode"
	"barcodereader/internal/httpapi"
	"barcodereader/internal/pdfembedded"
	"barcodereader/internal/pdfrender"
)

// Значения кодов в фикстурах — те же константы, что и в cmd/gendata.
const (
	valuePageOne     = "https://example.com/doc/page-1"
	valuePageThree   = "https://example.com/doc/page-3"
	valueDupSame     = "https://example.com/dup"
	valueMultiFirst  = "https://example.com/multi/first"
	valueMultiSecond = "https://example.com/multi/second"
	valueEAN         = "4601234567893"
	valueCode128     = "BARCODE-ONLY-VALUE"
)

var testRouter http.Handler

func TestMain(m *testing.M) {
	cfg := config.Default()
	cfg.PdfiumWorkers = 1
	cfg.RequestTimeout = 60_000_000_000 // 60s: рендер через wasm медленный
	cfg.MaxConcurrent = 2

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	renderer, err := pdfrender.NewRenderer(cfg.PdfiumWorkers)
	if err != nil {
		panic(err)
	}
	defer renderer.Close()

	decoder := decode.New(cfg, rendererAdapter{renderer}, pdfembedded.Extractor{}, logger)
	handler := httpapi.NewHandler(cfg, decoder, logger)
	testRouter = handler.Routes()

	os.Exit(m.Run())
}

// rendererAdapter закрывает конкретный тип рендерера интерфейсом пакета decode.
type rendererAdapter struct {
	renderer *pdfrender.Renderer
}

func (a rendererAdapter) Open(ctx context.Context, pdf []byte) (decode.Document, error) {
	return a.renderer.Open(ctx, pdf)
}

// fixture читает PDF из testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("фикстура %s недоступна (запустите go run ./cmd/gendata): %v", name, err)
	}
	return content
}

type apiResponse struct {
	RequestID string `json:"request_id"`
	Found     bool   `json:"found"`
	Value     string `json:"value"`
	Codes     []struct {
		Value  string `json:"value"`
		Kind   string `json:"kind"`
		Format string `json:"format"`
		Page   int    `json:"page"`
		Source string `json:"source"`
	} `json:"codes"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta struct {
		PagesTotal          int      `json:"pages_total"`
		PagesScanned        int      `json:"pages_scanned"`
		StagesUsed          []string `json:"stages_used"`
		CodesTotal          int      `json:"codes_total"`
		CodesUnique         int      `json:"codes_unique"`
		DuplicatesCollapsed int      `json:"duplicates_collapsed"`
	} `json:"meta"`
}

// post отправляет PDF в base64 и разбирает ответ.
func post(t *testing.T, pdf []byte, options map[string]any) (int, apiResponse) {
	t.Helper()

	payload := map[string]any{"pdf_base64": base64.StdEncoding.EncodeToString(pdf)}
	if options != nil {
		payload["options"] = options
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("маршалинг: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	testRouter.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	var parsed apiResponse
	raw, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("ответ не JSON (%s): %v", raw, err)
	}
	return res.StatusCode, parsed
}

func valuesOf(r apiResponse) map[string]string {
	out := map[string]string{}
	for _, code := range r.Codes {
		out[code.Value] = code.Kind
	}
	return out
}

// --- тесты ---

func TestDecodeVectorQR(t *testing.T) {
	status, res := post(t, fixture(t, "qr_vector.pdf"), nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	if res.Value != valuePageOne {
		t.Errorf("value = %q, ожидалось %q", res.Value, valuePageOne)
	}
	// Векторный QR не может быть извлечён как картинка — только рендер.
	if len(res.Meta.StagesUsed) == 0 || res.Meta.StagesUsed[0] != "rendered_page" {
		t.Errorf("stages_used = %v, ожидался рендер", res.Meta.StagesUsed)
	}
	if res.Codes[0].Source != "rendered_page" {
		t.Errorf("source = %q, ожидался rendered_page", res.Codes[0].Source)
	}
}

func TestDecodeEmbeddedPNG(t *testing.T) {
	status, res := post(t, fixture(t, "qr_embedded_png.pdf"), nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	if res.Value != valuePageOne {
		t.Errorf("value = %q", res.Value)
	}
	if res.Codes[0].Source != "embedded_image" {
		t.Errorf("source = %q, ожидался embedded_image (быстрый путь)", res.Codes[0].Source)
	}
}

func TestDecodeEmbeddedJPEG(t *testing.T) {
	status, res := post(t, fixture(t, "qr_embedded_jpeg.pdf"), nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	if res.Value != valuePageOne {
		t.Errorf("value = %q", res.Value)
	}
}

func TestDecodeScannedPage(t *testing.T) {
	status, res := post(t, fixture(t, "scan_page.pdf"), nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	if res.Value != valuePageOne {
		t.Errorf("value = %q, ожидалось %q", res.Value, valuePageOne)
	}
}

func TestDecodeMultipage(t *testing.T) {
	status, res := post(t, fixture(t, "qr_multipage.pdf"), nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}

	values := valuesOf(res)
	if values[valuePageOne] != "qr" || values[valuePageThree] != "qr" {
		t.Fatalf("найдено %v, ожидались оба QR", values)
	}

	// Главный код — QR на самой ранней странице.
	if res.Value != valuePageOne {
		t.Errorf("value = %q, ожидалось %q", res.Value, valuePageOne)
	}
	if res.Meta.PagesTotal != 3 {
		t.Errorf("pages_total = %d, ожидалось 3", res.Meta.PagesTotal)
	}
}

func TestDecodeMultipleQROnOnePage(t *testing.T) {
	status, res := post(t, fixture(t, "qr_multi_on_page.pdf"), nil)

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	values := valuesOf(res)
	if len(values) != 2 || values[valueMultiFirst] != "qr" || values[valueMultiSecond] != "qr" {
		t.Fatalf("codes = %v, ожидались оба QR", values)
	}
}

func TestDeduplicatesEqualValues(t *testing.T) {
	status, res := post(t, fixture(t, "dup_values.pdf"), map[string]any{"force_render": true})

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}

	// Один и тот же QR на трёх страницах плюс тот же текст в штрихкоде —
	// после свёртки остаётся единственная запись.
	if len(res.Codes) != 1 {
		t.Fatalf("после свёртки %d записей: %+v", len(res.Codes), res.Codes)
	}
	if res.Codes[0].Page != 1 {
		t.Errorf("page = %d, ожидалась 1", res.Codes[0].Page)
	}
	if res.Codes[0].Value != valueDupSame {
		t.Errorf("value = %q", res.Codes[0].Value)
	}
	if res.Meta.DuplicatesCollapsed == 0 {
		t.Errorf("duplicates_collapsed = 0, ожидалось > 0")
	}
	if res.Value != valueDupSame {
		t.Errorf("value = %q", res.Value)
	}
}

func TestDecodeQRAndEAN13(t *testing.T) {
	status, res := post(t, fixture(t, "barcode_ean13.pdf"), map[string]any{"include_barcodes": true})

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}

	values := valuesOf(res)
	if values[valuePageOne] != "qr" {
		t.Errorf("QR не найден: %v", values)
	}
	if values[valueEAN] != "barcode" {
		t.Errorf("EAN-13 не найден: %v", values)
	}
	if res.Value != valuePageOne {
		t.Errorf("value = %q, ожидалось значение QR", res.Value)
	}
}

func TestDecodeBarcodeOnly(t *testing.T) {
	status, res := post(t, fixture(t, "barcode_only.pdf"), map[string]any{"include_barcodes": true})

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	if res.Value != valueCode128 {
		t.Errorf("value = %q, ожидалось %q", res.Value, valueCode128)
	}
	if res.Codes[0].Kind != "barcode" {
		t.Errorf("kind = %q, ожидался barcode", res.Codes[0].Kind)
	}
}

// Поиск линейных кодов выключен по умолчанию: документ только со штрихкодом
// без явного include_barcodes должен быть признан пустым.
func TestBarcodesOffByDefault(t *testing.T) {
	for name, options := range map[string]map[string]any{
		"без опций":          nil,
		"явно выключены":     {"include_barcodes": false},
	} {
		t.Run(name, func(t *testing.T) {
			status, res := post(t, fixture(t, "barcode_only.pdf"), options)

			if status != http.StatusNotFound {
				t.Fatalf("status = %d, ожидался 404 (штрихкоды выключены)", status)
			}
			if res.Error.Code != "no_codes_found" {
				t.Errorf("code = %q", res.Error.Code)
			}
		})
	}
}

func TestNoCodesFound(t *testing.T) {
	status, res := post(t, fixture(t, "no_codes.pdf"), nil)

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, ожидался 404", status)
	}
	if res.Error.Code != "no_codes_found" {
		t.Errorf("code = %q, ожидалось no_codes_found", res.Error.Code)
	}
	if res.Found {
		t.Error("found должен быть false")
	}
}

func TestCorruptPDF(t *testing.T) {
	status, res := post(t, fixture(t, "corrupt.pdf"), nil)

	if status != http.StatusUnprocessableEntity && status != http.StatusNotFound {
		t.Fatalf("status = %d, ожидалось 422 или 404; тело: %+v", status, res.Error)
	}
	if status == http.StatusUnprocessableEntity && res.Error.Code != "pdf_unreadable" {
		t.Errorf("code = %q, ожидалось pdf_unreadable", res.Error.Code)
	}
}

func TestEmbeddedStageDisabled(t *testing.T) {
	// Проверяем деградацию: без стадии A документы находятся рендером.
	cfg := config.Default()
	cfg.EnableEmbeddedStage = false
	cfg.PdfiumWorkers = 1
	cfg.RequestTimeout = 60_000_000_000
	cfg.MaxConcurrent = 1

	renderer, err := pdfrender.NewRenderer(cfg.PdfiumWorkers)
	if err != nil {
		t.Fatalf("создание рендерера: %v", err)
	}
	t.Cleanup(renderer.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	decoder := decode.New(cfg, rendererAdapter{renderer}, pdfembedded.Extractor{}, logger)
	handler := httpapi.NewHandler(cfg, decoder, logger)

	pdf := fixture(t, "qr_embedded_png.pdf")
	body, err := json.Marshal(map[string]any{"pdf_base64": base64.StdEncoding.EncodeToString(pdf)})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.Routes().ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, тело: %s", rec.Result().StatusCode, rec.Body.String())
	}

	var res apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Value != valuePageOne {
		t.Errorf("value = %q, ожидалось %q", res.Value, valuePageOne)
	}
	if res.Codes[0].Source != "rendered_page" {
		t.Errorf("source = %q, ожидался rendered_page", res.Codes[0].Source)
	}
}

func TestPageSelection(t *testing.T) {
	// Запрашиваем только третью страницу многостраничного документа.
	status, res := post(t, fixture(t, "qr_multipage.pdf"), map[string]any{"pages": "3"})

	if status != http.StatusOK {
		t.Fatalf("status = %d, тело: %+v", status, res.Error)
	}
	if res.Value != valuePageThree {
		t.Errorf("value = %q, ожидалось %q (стр. 3)", res.Value, valuePageThree)
	}
	if len(res.Codes) != 1 {
		t.Errorf("codes = %d, ожидалась 1 запись", len(res.Codes))
	}
}

func TestRawPDFBody(t *testing.T) {
	pdf := fixture(t, "qr_embedded_png.pdf")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", bytes.NewReader(pdf))
	req.Header.Set("Content-Type", "application/pdf")
	rec := httptest.NewRecorder()
	testRouter.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, тело: %s", rec.Result().StatusCode, rec.Body.String())
	}
}

func TestConcurrentRequests(t *testing.T) {
	pdf := fixture(t, "qr_embedded_png.pdf")

	const parallel = 4
	type outcome struct {
		status int
		body   apiResponse
		err    error
	}
	results := make(chan outcome, parallel)

	for range parallel {
		go func() {
			status, body := post(t, pdf, nil)
			results <- outcome{status: status, body: body}
		}()
	}

	for range parallel {
		got := <-results
		if got.err != nil {
			t.Fatalf("параллельный запрос: %v", got.err)
		}
		if got.status != http.StatusOK {
			t.Errorf("status = %d, ожидалось 200", got.status)
		}
		if got.body.Value != valuePageOne {
			t.Errorf("value = %q", got.body.Value)
		}
	}
}