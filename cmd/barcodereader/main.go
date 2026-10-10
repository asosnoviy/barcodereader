// Command barcodereader запускает HTTP-сервис декодирования QR-кодов и
// штрихкодов из PDF-документов.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"barcodereader/internal/config"
	"barcodereader/internal/decode"
	"barcodereader/internal/httpapi"
	"barcodereader/internal/pdfembedded"
	"barcodereader/internal/pdfrender"
)

func main() {
	if err := run(); err != nil {
		slog.Error("сервис остановлен с ошибкой", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	// Пул PDFium инициализируется один раз: Wazero компилирует wasm-модуль
	// при старте, поэтому лишние инстансы замедляют подъём сервиса.
	renderer, err := pdfrender.NewRenderer(cfg.PdfiumWorkers)
	if err != nil {
		return err
	}
	defer renderer.Close()

	decoder := decode.New(cfg, pdfiumRenderer{renderer}, pdfembedded.Extractor{}, logger)
	handler := httpapi.NewHandler(cfg, decoder, logger)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler.Routes(),
		// WriteTimeout должен быть заметно больше таймаута декодирования,
		// иначе соединение оборвётся раньше, чем сформируется ответ.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 10*time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("сервис запущен",
			"addr", cfg.Addr,
			"workers", cfg.PdfiumWorkers,
			"embedded_stage", cfg.EnableEmbeddedStage,
			"render_dpi", cfg.RenderDPI,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("получен сигнал остановки")
	}

	// Новые запросы отклоняются сразу, текущие успевают завершиться.
	handler.StartDraining()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("сервис остановлен")
	return nil
}

// pdfiumRenderer адаптирует pdfrender.Renderer к интерфейсам decode.
type pdfiumRenderer struct {
	renderer *pdfrender.Renderer
}

func (p pdfiumRenderer) Open(ctx context.Context, pdf []byte) (decode.Document, error) {
	return p.renderer.Open(ctx, pdf)
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}