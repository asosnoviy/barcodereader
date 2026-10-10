package config

import (
	"strings"
	"testing"
	"time"
)

func envFrom(m map[string]string) func(string) string {
	return func(name string) string { return m[name] }
}

func TestLoadFromDefaults(t *testing.T) {
	cfg, err := LoadFrom(envFrom(nil))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, ожидалось \":8080\"", cfg.Addr)
	}
	if cfg.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %s, ожидалось 30s", cfg.RequestTimeout)
	}
	if cfg.MaxPDFBytes != 20<<20 {
		t.Errorf("MaxPDFBytes = %d, ожидалось %d", cfg.MaxPDFBytes, 20<<20)
	}
	if cfg.MaxPages != 10 {
		t.Errorf("MaxPages = %d, ожидалось 10", cfg.MaxPages)
	}
	if cfg.RenderDPI != 200 || cfg.RenderDPIMax != 400 {
		t.Errorf("DPI = %d/%d, ожидалось 200/400", cfg.RenderDPI, cfg.RenderDPIMax)
	}
	if !cfg.EnableEmbeddedStage || !cfg.IncludeBarcodes || !cfg.ReturnAllQR {
		t.Error("стадии и декодирование должны быть включены по умолчанию")
	}
	if cfg.PdfiumWorkers < 1 || cfg.MaxConcurrent < 1 {
		t.Errorf("Workers/MaxConcurrent должны быть >= 1, получено %d/%d", cfg.PdfiumWorkers, cfg.MaxConcurrent)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, ожидалось \"info\"", cfg.LogLevel)
	}
	if cfg.LogPayloads {
		t.Error("LogPayloads по умолчанию должен быть выключен")
	}
}

func TestLoadFromOverrides(t *testing.T) {
	cfg, err := LoadFrom(envFrom(map[string]string{
		"ADDR":                 "127.0.0.1:9999",
		"REQUEST_TIMEOUT":      "12s",
		"MAX_PDF_BYTES":        "1048576",
		"MAX_BODY_BYTES":       "2097152",
		"MAX_PAGES":            "3",
		"RENDER_DPI":           "150",
		"RENDER_DPI_MAX":       "600",
		"RETRY_HIGHER_DPI":     "false",
		"MAX_IMAGE_DIM":        "1200",
		"ONED_MAX_WIDTH":       "800",
		"ENABLE_EMBEDDED_STAGE": "false",
		"INCLUDE_BARCODES":     "0",
		"RETURN_ALL_QR":        "no",
		"MAX_RENDER_PIXELS":    "1000000",
		"PDFIUM_WORKERS":       "2",
		"MAX_CONCURRENT":       "5",
		"QUEUE_WAIT":           "500ms",
		"LOG_LEVEL":            "debug",
		"LOG_PAYLOADS":         "true",
	}))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Addr != "127.0.0.1:9999" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.RequestTimeout != 12*time.Second {
		t.Errorf("RequestTimeout = %s", cfg.RequestTimeout)
	}
	if cfg.MaxPDFBytes != 1<<20 || cfg.MaxBodyBytes != 2<<20 {
		t.Errorf("лимиты размеров не применены: %d/%d", cfg.MaxPDFBytes, cfg.MaxBodyBytes)
	}
	if cfg.MaxPages != 3 || cfg.MaxImageDim != 1200 || cfg.OneDMaxWidth != 800 {
		t.Errorf("числовые лимиты не применены: %+v", cfg)
	}
	if cfg.RenderDPI != 150 || cfg.RenderDPIMax != 600 || cfg.RetryHigherDPI {
		t.Errorf("DPI-настройки не применены: %+v", cfg)
	}
	if cfg.EnableEmbeddedStage || cfg.IncludeBarcodes || cfg.ReturnAllQR {
		t.Errorf("булевы настройки не применены: %+v", cfg)
	}
	if cfg.MaxRenderPixels != 1_000_000 || cfg.PdfiumWorkers != 2 || cfg.MaxConcurrent != 5 {
		t.Errorf("настройки пула не применены: %+v", cfg)
	}
	if cfg.QueueWait != 500*time.Millisecond {
		t.Errorf("QueueWait = %s", cfg.QueueWait)
	}
	if cfg.LogLevel != "debug" || !cfg.LogPayloads {
		t.Errorf("настройки логирования не применены: %+v", cfg)
	}
}

func TestLoadFromRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"нечисловой таймаут", map[string]string{"REQUEST_TIMEOUT": "быстро"}, "REQUEST_TIMEOUT"},
		{"нулевой лимит", map[string]string{"MAX_PDF_BYTES": "0"}, "MAX_PDF_BYTES"},
		{"отрицательные страницы", map[string]string{"MAX_PAGES": "-1"}, "MAX_PAGES"},
		{"неизвестный уровень лога", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"не булево", map[string]string{"INCLUDE_BARCODES": "возможно"}, "INCLUDE_BARCODES"},
		{"DPI_MAX меньше DPI", map[string]string{"RENDER_DPI": "400", "RENDER_DPI_MAX": "200"}, "RENDER_DPI_MAX"},
		{"нулевые воркеры", map[string]string{"PDFIUM_WORKERS": "0"}, "PDFIUM_WORKERS"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFrom(envFrom(tc.env))
			if err == nil {
				t.Fatal("ожидалась ошибка валидации")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ошибка %q не упоминает %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadFromCollectsAllErrors(t *testing.T) {
	_, err := LoadFrom(envFrom(map[string]string{
		"MAX_PAGES":     "0",
		"REQUEST_TIMEOUT": "нет",
	}))
	if err == nil {
		t.Fatal("ожидалась ошибка валидации")
	}
	msg := err.Error()
	if !strings.Contains(msg, "MAX_PAGES") || !strings.Contains(msg, "REQUEST_TIMEOUT") {
		t.Errorf("ожидались обе ошибки, получено: %q", msg)
	}
}

func TestLoadUsesProcessEnvironment(t *testing.T) {
	t.Setenv("ADDR", "127.0.0.1:1234")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:1234" {
		t.Errorf("Addr = %q, ожидалось значение из окружения", cfg.Addr)
	}
}