// Package app содержит запуск HTTP-сервиса декодирования кодов.
//
// Логика отделена от main, потому что её используют два способа старта:
// обычный запуск из консоли и запуск под службой Windows. Служба получает
// команды управления от SCM, а консоль — сигналы; общая часть (создание
// пула PDFium, HTTP-сервера, корректное завершение) должна быть одна.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"barcodereader/internal/buildinfo"
	"barcodereader/internal/config"
	"barcodereader/internal/decode"
	"barcodereader/internal/httpapi"
	"barcodereader/internal/pdfembedded"
	"barcodereader/internal/pdfrender"
)

// executablePath — путь к запущенному бинарнику. Служба поднимает файл
// из Program Files, разработчик собирает в bin/: без этого в журнале
// нельзя отличить одно от другого.
func executablePath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "неизвестно"
}

// workingDir — рабочий каталог процесса. У службы он системный, и это
// неожиданно для того, кто привык запускать из своего каталога.
func workingDir() string {
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "неизвестно"
}

// currentUser — пользователь, от имени которого работает процесс.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USERNAME"); name != "" {
		return name
	}
	return "неизвестно"
}

// tempDirWritable проверяет, доступен ли временный каталог для записи.
// Некоторые библиотеки туда пишут, и отказ оборачивается ошибкой,
// не связанной с самим документом.
func tempDirWritable() string {
	dir := os.TempDir()
	probe, err := os.CreateTemp(dir, "probe-*")
	if err != nil {
		return "нет (" + err.Error() + ")"
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return filepath.Clean(dir) + " доступен"
}

// ShutdownTimeout — сколько ждать завершения текущих запросов после
// команды на остановку.
const ShutdownTimeout = 15 * time.Second

// Run запускает сервис и блокируется до отмены контекста или ошибки.
//
// В отличие от прежнего варианта в main.go, остановка происходит по
// контексту, а не по сигналу: служба Windows не присылает сигналы, ей
// приходит команда Stop от SCM. Сигналы по-прежнему обрабатываются —
// консольный запуск ведёт себя как раньше.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
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

	errCh := make(chan error, 1)
	go func() {
		logger.Info("сервис запущен",
			"addr", cfg.Addr,
			"pid", os.Getpid(),
			"version", buildinfo.String(),
			"binary", executablePath(),
			"working_dir", workingDir(),
			"user", currentUser(),
			"temp_dir", tempDirWritable(),
			"workers", cfg.PdfiumWorkers,
			"embedded_stage", cfg.EnableEmbeddedStage,
			"render_dpi", cfg.RenderDPI,
			"log_file", cfg.LogFile,
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("получена команда остановки")
	}

	// Новые запросы отклоняются сразу, текущие успевают завершиться.
	handler.StartDraining()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("сервис остановлен")
	return nil
}

// RunWithSignals запускает сервис, перехватывая сигналы остановки.
// Это консольный сценарий: Ctrl+C и SIGTERM должны завершать работу.
func RunWithSignals(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, cfg, logger)
}

// pdfiumRenderer адаптирует pdfrender.Renderer к интертефейсам decode.
type pdfiumRenderer struct {
	renderer *pdfrender.Renderer
}

func (p pdfiumRenderer) Open(ctx context.Context, pdf []byte) (decode.Document, error) {
	return p.renderer.Open(ctx, pdf)
}