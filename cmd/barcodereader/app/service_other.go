//go:build !windows

package app

import (
	"context"
	"log/slog"

	"barcodereader/internal/config"
)

// RunService на не-Windows системах запускает сервис как обычно.
//
// Отдельный файл нужен, чтобы код со сборкой только под Windows не попал
// в сборку для Linux: пакет windows/svc там просто не компилируется.
// Служб в Linux нет, и определять режим запуска незачем.
func RunService(cfg config.Config, logger *Logger) error {
	return RunWithSignals(cfg, logger)
}

func currentPID() int { return 0 }

func newServiceContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

var _ = slog.LevelInfo