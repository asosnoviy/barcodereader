// Command barcodereader запускает HTTP-сервис декодирования QR-кодов и
// штрихкодов из PDF-документов.
//
// Одна и та же программа работает двумя способами: как обычное приложение
// из консоли и как служба Windows. Способ определяется автоматически, по
// тому запустил ли процесс диспетчер служб, поэтому отдельного ключа
// запуска не требуется.
package main

import (
	"io"
	"log/slog"
	"os"

	"barcodereader/cmd/barcodereader/app"
	"barcodereader/internal/config"
)

func main() {
	cfg, logger, closer, err := prepare()
	if err != nil {
		// На этом этапе надёжного журнала ещё нет, поэтому причина уходит
		// в stderr: при запуске из консоли её видно сразу.
		slog.Default().Error("не удалось запустить сервис", "err", err)
		os.Exit(1)
	}
	defer closer.Close()

	if err := app.RunService(cfg, logger); err != nil {
		logger.Error("сервис остановлен с ошибкой", "err", err)
		os.Exit(1)
	}
}

// prepare читает конфигурацию и поднимает журнал.
//
// Куда писать журнал, решает RunService: он узнаёт режим запуска раньше
// всего и для службы переключает журнал в файл. Здесь журнал ещё в stdout —
// иначе обычный запуск из консоли молча писал бы в файл вместо окна.
func prepare() (config.Config, *slog.Logger, io.Closer, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, nil, nil, err
	}

	// Явный LOG_FILE уважается сразу: им пользуются, когда сервис
	// запускают из планировщика задач без окна.
	logger, closer, err := app.NewLogger(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		// Недоступный журнал — не повод не стартовать: продолжаем в stdout,
		// но причину сообщаем явно.
		logger, closer, fallbackErr := app.NewLogger(cfg.LogLevel, "")
		if fallbackErr != nil {
			return config.Config{}, nil, nil, fallbackErr
		}
		logger.Warn("журнал в файл недоступен, пишу в stdout", "err", err)
		return cfg, logger, closer, nil
	}

	logger.Debug("конфигурация загружена",
		"config_file", config.ConfigFilePath(),
		"addr", cfg.Addr,
	)
	return cfg, logger, closer, nil
}