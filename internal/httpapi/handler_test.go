package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"barcodereader/internal/apierrors"
	"barcodereader/internal/config"
	"barcodereader/internal/decode"
)

// stubDecoder возвращает заранее заданный результат или ошибку.
type stubDecoder struct {
	result *decode.Result
	err    error
	gotPDF []byte
	opts   decode.Options
	calls  int
}

func (s *stubDecoder) Decode(_ context.Context, pdf []byte, opts decode.Options) (*decode.Result, error) {
	s.calls++
	s.gotPDF = pdf
	s.opts = opts
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func testHandler(decoder Decoder, tune func(*config.Config)) *Handler {
	cfg := config.Default()
	if tune != nil {
		tune(&cfg)
	}
	return NewHandler(cfg, decoder, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func validPDF() []byte {
	return []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n%%EOF")
}

func decodeBody(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("чтение тела ответа: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("ответ не является JSON (%s): %v", body, err)
	}
	return parsed
}

func postJSON(t *testing.T, h *Handler, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("маршалинг запроса: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec.Result()
}

// Журнал службы по умолчанию открывают на уровне warn, и в нём должны
// оставаться только старт, остановка и ошибки. Запись о каждом успешном
// сканировании при таком уровне недопустима: на info она тоже полезна,
// поэтому проверяем обе границы.
func TestSuccessfulDecodeLogging(t *testing.T) {
	newHandler := func(level slog.Level) (*Handler, *bytes.Buffer) {
		var buf bytes.Buffer
		decoder := &stubDecoder{result: &decode.Result{
			Found: true,
			Value: "https://example.com/42",
			Codes: []decode.Code{{
				Value: "https://example.com/42", Kind: "qr", Format: "QR_CODE",
				Page: 1, Source: decode.SourceEmbeddedImage,
			}},
			Meta: decode.Meta{PagesTotal: 1, PagesScanned: 1, StagesUsed: []string{"embedded_image"}, CodesUnique: 1},
		}}
		handler := NewHandler(config.Default(), decoder,
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
		return handler, &buf
	}

	t.Run("warn по умолчанию молчит", func(t *testing.T) {
		h, buf := newHandler(slog.LevelWarn)
		res := postJSON(t, h, decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(validPDF())})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, ожидалось 200", res.StatusCode)
		}
		if buf.Len() != 0 {
			t.Errorf("успешное сканирование попало в журнал уровня warn: %s", buf.String())
		}
	})

	t.Run("info пишет каждое сканирование", func(t *testing.T) {
		h, buf := newHandler(slog.LevelInfo)
		res := postJSON(t, h, decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(validPDF())})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, ожидалось 200", res.StatusCode)
		}
		if !strings.Contains(buf.String(), "документ обработан") {
			t.Errorf("на уровне info нет записи о сканировании: %s", buf.String())
		}
	})
}

func TestDecodeSuccess(t *testing.T) {
	decoder := &stubDecoder{result: &decode.Result{
		Found: true,
		Value: "https://example.com/42",
		Codes: []decode.Code{{
			Value: "https://example.com/42", Kind: "qr", Format: "QR_CODE",
			Page: 1, Source: decode.SourceEmbeddedImage, ImageObject: 7,
		}},
		Meta: decode.Meta{PagesTotal: 1, PagesScanned: 1, DPI: 200, StagesUsed: []string{"embedded_image"}, CodesUnique: 1},
	}}
	h := testHandler(decoder, nil)

	res := postJSON(t, h, decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(validPDF())})

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, ожидалось 200", res.StatusCode)
	}
	body := decodeBody(t, res)

	if body["found"] != true {
		t.Errorf("found = %v, ожидалось true", body["found"])
	}
	if body["value"] != "https://example.com/42" {
		t.Errorf("value = %v", body["value"])
	}
	codes, ok := body["codes"].([]any)
	if !ok || len(codes) != 1 {
		t.Fatalf("codes = %v, ожидалась одна запись", body["codes"])
	}
	first := codes[0].(map[string]any)
	if first["kind"] != "qr" || first["format"] != "QR_CODE" {
		t.Errorf("запись кода = %v", first)
	}
	if first["image_object"].(float64) != 7 {
		t.Errorf("image_object = %v, ожидалось 7", first["image_object"])
	}
	if body["request_id"] == "" || body["request_id"] == nil {
		t.Error("request_id пуст")
	}
}

func TestCodesIsEmptyArrayNotNull(t *testing.T) {
	decoder := &stubDecoder{result: &decode.Result{
		Found: true, Value: "X",
		Codes: []decode.Code{{Value: "X", Kind: "qr", Format: "QR_CODE", Page: 1, Source: decode.SourceRenderedPage}},
		Meta:  decode.Meta{StagesUsed: []string{}},
	}}
	h := testHandler(decoder, nil)

	res := postJSON(t, h, decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(validPDF())})
	body := decodeBody(t, res)

	if _, ok := body["codes"].([]any); !ok {
		t.Errorf("codes должен быть массивом, получено %T", body["codes"])
	}
}

func TestRawPDFBody(t *testing.T) {
	decoder := &stubDecoder{result: &decode.Result{Found: true, Value: "X", Codes: []decode.Code{}, Meta: decode.Meta{}}}
	h := testHandler(decoder, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", bytes.NewReader(validPDF()))
	req.Header.Set("Content-Type", "application/pdf")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, ожидалось 200", rec.Result().StatusCode)
	}
	if !bytes.Equal(decoder.gotPDF, validPDF()) {
		t.Error("декодировщик получил не исходные байты PDF")
	}
}

func TestBase64Variants(t *testing.T) {
	pdf := validPDF()
	decoder := &stubDecoder{result: &decode.Result{Found: true, Value: "X", Codes: []decode.Code{}, Meta: decode.Meta{}}}
	h := testHandler(decoder, nil)

	variants := map[string]string{
		"std с переводами строк": insertNewlines(base64.StdEncoding.EncodeToString(pdf)),
		"raw без padding":       base64.RawStdEncoding.EncodeToString(pdf),
		"url-безопасный":        base64.RawURLEncoding.EncodeToString(pdf),
		"с пробелами":           " " + base64.StdEncoding.EncodeToString(pdf) + " ",
	}

	for name, encoded := range variants {
		t.Run(name, func(t *testing.T) {
			res := postJSON(t, h, decodeRequest{PDFBase64: encoded})
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, ожидалось 200 (тело %v)", res.StatusCode, decodeBody(t, res))
			}
		})
	}
}

func TestErrorCases(t *testing.T) {
	tests := []struct {
		name       string
		payload    any
		rawBody    string
		contentType string
		tune       func(*config.Config)
		wantStatus int
		wantCode   string
	}{
		{
			name:        "невалидный JSON",
			rawBody:     "{не json",
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_json",
		},
		{
			name:        "пустое поле",
			payload:     decodeRequest{PDFBase64: ""},
			wantStatus:  http.StatusBadRequest,
			wantCode:    "missing_field",
		},
		{
			name:        "битый base64",
			payload:     decodeRequest{PDFBase64: "не-base64!!!"},
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_base64",
		},
		{
			name:        "не PDF",
			payload:     decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString([]byte("привет"))},
			wantStatus:  http.StatusBadRequest,
			wantCode:    "not_a_pdf",
		},
		{
			name:        "неподдерживаемый content-type",
			rawBody:     "{}",
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "unsupported_media_type",
		},
		{
			name:       "слишком большой документ",
			payload:    decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(make([]byte, 300))},
			tune:      func(c *config.Config) { c.MaxPDFBytes = 100 },
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "payload_too_large",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler(&stubDecoder{}, tc.tune)

			var res *http.Response
			switch {
			case tc.payload != nil:
				res = postJSON(t, h, tc.payload)
			default:
				req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", strings.NewReader(tc.rawBody))
				contentType := tc.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				req.Header.Set("Content-Type", contentType)
				rec := httptest.NewRecorder()
				h.Routes().ServeHTTP(rec, req)
				res = rec.Result()
			}

			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, ожидалось %d", res.StatusCode, tc.wantStatus)
			}
			body := decodeBody(t, res)
			if body["found"] != false {
				t.Errorf("found = %v, ожидалось false", body["found"])
			}
			errObj, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("нет объекта error: %v", body)
			}
			if errObj["code"] != tc.wantCode {
				t.Errorf("error.code = %v, ожидалось %q", errObj["code"], tc.wantCode)
			}
		})
	}
}

func TestOversizedBody(t *testing.T) {
	h := testHandler(&stubDecoder{}, func(c *config.Config) { c.MaxBodyBytes = 64 })

	payload, err := json.Marshal(decodeRequest{PDFBase64: strings.Repeat("A", 200)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/decode", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, ожидалось 413", res.StatusCode)
	}
	if decodeBody(t, res)["error"].(map[string]any)["code"] != "payload_too_large" {
		t.Error("ожидался код payload_too_large")
	}
}

func TestDecoderErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"коды не найдены", apierrors.NoCodesFound(map[string]any{"pages_scanned": 2}), http.StatusNotFound, "no_codes_found"},
		{"зашифрован", apierrors.PDFEncrypted(errors.New("x")), http.StatusUnprocessableEntity, "pdf_encrypted"},
		{"повреждён", apierrors.PDFUnreadable(errors.New("x")), http.StatusUnprocessableEntity, "pdf_unreadable"},
		{"таймаут", apierrors.Timeout(), http.StatusGatewayTimeout, "timeout"},
		{"перегрузка", apierrors.TooManyRequests("2s"), http.StatusTooManyRequests, "too_many_requests"},
		{"нет ёмкости", apierrors.NoCapacity(errors.New("x")), http.StatusServiceUnavailable, "no_capacity"},
		{"внутренняя", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler(&stubDecoder{err: tc.err}, nil)
			res := postJSON(t, h, decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(validPDF())})

			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, ожидалось %d", res.StatusCode, tc.wantStatus)
			}
			if got := decodeBody(t, res)["error"].(map[string]any)["code"]; got != tc.wantCode {
				t.Errorf("code = %v, ожидалось %q", got, tc.wantCode)
			}
		})
	}
}

func TestOptionsArePassedThrough(t *testing.T) {
	decoder := &stubDecoder{result: &decode.Result{Found: true, Value: "X", Codes: []decode.Code{}, Meta: decode.Meta{}}}
	h := testHandler(decoder, nil)

	includeBarcodes := false
	dpi := 300
	maxPages := 4

	res := postJSON(t, h, decodeRequest{
		PDFBase64: base64.StdEncoding.EncodeToString(validPDF()),
		Options: &requestOption{
			Pages:           "1-3,7",
			IncludeBarcodes: &includeBarcodes,
			DPI:             &dpi,
			MaxPages:        &maxPages,
			ForceRender:     true,
		},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	if got := decoder.opts.Pages; len(got) != 4 || got[0] != 1 || got[3] != 7 {
		t.Errorf("pages = %v, ожидалось [1 2 3 7]", got)
	}
	if decoder.opts.IncludeBarcodes {
		t.Error("include_barcodes=false не передан")
	}
	if decoder.opts.DPI != 300 || decoder.opts.MaxPages != 4 || !decoder.opts.ForceRender {
		t.Errorf("опции не передан: %+v", decoder.opts)
	}
}

func TestInvalidPageSelection(t *testing.T) {
	h := testHandler(&stubDecoder{}, nil)

	for _, selection := range []string{"abc", "5-1", "1,,2", "0", "-3"} {
		res := postJSON(t, h, decodeRequest{
			PDFBase64: base64.StdEncoding.EncodeToString(validPDF()),
			Options:   &requestOption{Pages: selection},
		})
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("selection %q: status = %d, ожидалось 400", selection, res.StatusCode)
			continue
		}
		if got := decodeBody(t, res)["error"].(map[string]any)["code"]; got != "invalid_pages" {
			t.Errorf("selection %q: code = %v", selection, got)
		}
	}
}

func TestHealthAndReady(t *testing.T) {
	h := testHandler(&stubDecoder{}, nil)
	mux := h.Routes()

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Result().StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d", path, rec.Result().StatusCode)
		}
	}
}

func TestReadyReturns503WhileDraining(t *testing.T) {
	h := testHandler(&stubDecoder{}, nil)
	h.StartDraining()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, ожидалось 503", rec.Result().StatusCode)
	}

	res := postJSON(t, h, decodeRequest{PDFBase64: base64.StdEncoding.EncodeToString(validPDF())})
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("decode во время drain: status = %d, ожидалось 503", res.StatusCode)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := testHandler(&stubDecoder{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/decode", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, ожидалось 405", rec.Result().StatusCode)
	}
}

func insertNewlines(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%40 == 0 {
			b.WriteByte('\n')
		}
		b.WriteRune(r)
	}
	return b.String()
}