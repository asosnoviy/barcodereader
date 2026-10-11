// Package config загружает и валидирует конфигурацию сервиса из переменных
// окружения. Флагов и файлов конфигурации нет намеренно: это упрощает деплой.
package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Config содержит все параметры сервиса.
type Config struct {
	Addr                string
	RequestTimeout      time.Duration
	MaxPDFBytes         int64
	MaxBodyBytes        int64
	MaxPages            int
	MaxRenderPixels     int64
	RenderDPI           int
	RenderDPIMax        int
	RetryHigherDPI      bool
	MaxImageDim         int
	OneDMaxWidth        int
	RegionSearch        bool
	RegionMargin        int
	EnableEmbeddedStage bool
	IncludeBarcodes     bool
	ReturnAllQR         bool
	PdfiumWorkers       int
	MaxConcurrent       int
	QueueWait           time.Duration
	LogLevel            string
	LogPayloads         bool
	LogFile             string
}

// Default возвращает конфигурацию по умолчанию.
func Default() Config {
	workers := runtime.NumCPU()
	switch {
	case workers > 4:
		workers = 4
	case workers < 1:
		workers = 1
	}

	return Config{
		Addr:                ":8080",
		RequestTimeout:      30 * time.Second,
		MaxPDFBytes:         20 << 20,
		MaxBodyBytes:        30 << 20,
		MaxPages:            10,
		MaxRenderPixels:     26_000_000,
		RenderDPI:           200,
		RenderDPIMax:        400,
		RetryHigherDPI:      true,
		MaxImageDim:         3000,
		OneDMaxWidth:        1600,
		RegionSearch:        true,
		RegionMargin:        8,
		EnableEmbeddedStage: true,
		IncludeBarcodes:     true,
		ReturnAllQR:         true,
		PdfiumWorkers:       workers,
		MaxConcurrent:       workers,
		QueueWait:           2 * time.Second,
		LogLevel:            "warn",
		LogPayloads:         false,
	}
}

// Load читает конфигурацию: сначала файл конфигурации (если он есть),
// поверх — переменные окружения. Переменные имеют приоритет.
func Load() (Config, error) { return LoadFrom(environmentWithFile(os.Getenv)) }

// LoadFrom читает конфигурацию из переданной функции доступа к переменным
// окружения. Ошибки валидации собираются все сразу, а не по одной.
func LoadFrom(getenv func(string) string) (Config, error) {
	cfg := Default()
	var errs []string

	duration := func(name string, dst *time.Duration, min time.Duration) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return
		}
		v, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %q не является длительностью", name, raw))
			return
		}
		if v < min {
			errs = append(errs, fmt.Sprintf("%s: %s меньше минимума %s", name, v, min))
			return
		}
		*dst = v
	}

	positiveInt := func(name string, dst *int) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %q не является целым числом", name, raw))
			return
		}
		if v <= 0 {
			errs = append(errs, fmt.Sprintf("%s: должно быть > 0, получено %d", name, v))
			return
		}
		*dst = v
	}

	positiveInt64 := func(name string, dst *int64) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %q не является целым числом", name, raw))
			return
		}
		if v <= 0 {
			errs = append(errs, fmt.Sprintf("%s: должно быть > 0, получено %d", name, v))
			return
		}
		*dst = v
	}

	boolean := func(name string, dst *bool) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return
		}
		// Кроме значений strconv принимаем распространённые в docker-compose
		// и .env варианты: yes/no, on/off.
		switch strings.ToLower(raw) {
		case "1", "t", "true", "y", "yes", "on":
			*dst = true
			return
		case "0", "f", "false", "n", "no", "off":
			*dst = false
			return
		}
		errs = append(errs, fmt.Sprintf("%s: %q не является булевым значением", name, raw))
	}

	if v := strings.TrimSpace(getenv("ADDR")); v != "" {
		cfg.Addr = v
	}

	duration("REQUEST_TIMEOUT", &cfg.RequestTimeout, time.Second)
	duration("QUEUE_WAIT", &cfg.QueueWait, 0)

	positiveInt64("MAX_PDF_BYTES", &cfg.MaxPDFBytes)
	positiveInt64("MAX_BODY_BYTES", &cfg.MaxBodyBytes)
	positiveInt64("MAX_RENDER_PIXELS", &cfg.MaxRenderPixels)
	positiveInt("MAX_PAGES", &cfg.MaxPages)
	positiveInt("MAX_IMAGE_DIM", &cfg.MaxImageDim)
	positiveInt("ONED_MAX_WIDTH", &cfg.OneDMaxWidth)
	positiveInt("PDFIUM_WORKERS", &cfg.PdfiumWorkers)
	positiveInt("MAX_CONCURRENT", &cfg.MaxConcurrent)

	// Журнал в файл: пустой LOG_FILE означает stdout, как и раньше.
	if v := strings.TrimSpace(getenv("LOG_FILE")); v != "" {
		cfg.LogFile = v
	}

	// Поле тишины вокруг найденной области. 0 включает автоматический выбор:
	// поле считается четвертью стороны символа.
	if v := strings.TrimSpace(getenv("REGION_MARGIN")); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			errs = append(errs, fmt.Sprintf("REGION_MARGIN: %q должно быть целым и >= 0", v))
		} else {
			cfg.RegionMargin = parsed
		}
	}

	// DPI применяются только парой: если одна из настроек некорректна, обе
	// остаются дефолтными, чтобы лестница рендера не оказалась перевёрнутой.
	dpi, dpiMax := cfg.RenderDPI, cfg.RenderDPIMax
	dpiErrs := len(errs)
	positiveInt("RENDER_DPI", &dpi)
	positiveInt("RENDER_DPI_MAX", &dpiMax)
	if len(errs) == dpiErrs {
		if dpiMax < dpi {
			errs = append(errs, fmt.Sprintf("RENDER_DPI_MAX (%d) меньше RENDER_DPI (%d)", dpiMax, dpi))
		} else {
			cfg.RenderDPI, cfg.RenderDPIMax = dpi, dpiMax
		}
	}

	boolean("RETRY_HIGHER_DPI", &cfg.RetryHigherDPI)
	boolean("ENABLE_EMBEDDED_STAGE", &cfg.EnableEmbeddedStage)
	boolean("REGION_SEARCH", &cfg.RegionSearch)
	boolean("INCLUDE_BARCODES", &cfg.IncludeBarcodes)
	boolean("RETURN_ALL_QR", &cfg.ReturnAllQR)
	boolean("LOG_PAYLOADS", &cfg.LogPayloads)

	if v := strings.ToLower(strings.TrimSpace(getenv("LOG_LEVEL"))); v != "" {
		switch v {
		case "debug", "info", "warn", "error":
			cfg.LogLevel = v
		default:
			errs = append(errs, fmt.Sprintf("LOG_LEVEL: %q, допустимо debug|info|warn|error", v))
		}
	}

	if len(errs) > 0 {
		return cfg, fmt.Errorf("некорректная конфигурация: %s", strings.Join(errs, "; "))
	}
	return cfg, nil
}