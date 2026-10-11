// Command barcodereader запускает HTTP-сервис декодирования QR-кодов и
// штрихкодов из PDF-документов.
//
// Одна и та же программа работает двумя способами: как обычное приложение
// из консоли и как служба Windows. Способ определяется автоматически, по
// тому запустил ли процесс диспетчер служб, поэтому отдельного ключа
// запуска не требуется.
package main

import (
	"os"

	"barcodereader/cmd/barcodereader/app"
	"barcodereader/internal/config"
)

func main() {
	cfg, logger, err := prepare()
	if err != nil {
		// На этом этапе надёжного журнала ещё нет, поэтому причина уходит
		// в stderr: при запуске из консоли её видно сразу.
		app.New("info").Error("не удалось запустить сервис", "err", err)
		os.Exit(1)
	}
	defer logger.Close()

	if err := app.RunService(cfg, logger); err != nil {
		logger.Error("сервис остановлен с ошибкой", "err", err)
		os.Exit(1)
	}
}

// prepare читает конфигурацию и поднимает журнал.
//
// Журнал создаётся сразу, до определения режима запуска, и перенаправляется
// в файл уже внутри RunService — в момент, когда выясняется, что stdout
// читать некому. Подмена журнала целиком здесь не годилась бы: обработчик
// службы получает его раньше, чем известно, что мы под SCM.
func prepare() (config.Config, *app.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, nil, err
	}

	logger := app.New(cfg.LogLevel)

	// Явный LOG_FILE уважается сразу: им пользуются, когда сервис
	// запускают из планировщика задач без окна.
	if path := cfg.LogFile; path != "" {
		if rerr := logger.RedirectTo(path); rerr != nil {
			logger.Warn("журнал в файл недоступен, пишу в stdout", "err", rerr)
		}
	}

	logger.Debug("конфигурация загружена",
		"config_file", config.ConfigFilePath(),
		"addr", cfg.Addr,
	)
	return cfg, logger, nil
}