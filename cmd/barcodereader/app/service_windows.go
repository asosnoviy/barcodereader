//go:build windows

package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"

	"barcodereader/internal/config"
)

// ServiceName — имя службы в диспетчере служб Windows.
//
// Совпадает с именем, под которым служба регистрируется через sc.exe:
//   sc.exe create BarcodeReader binPath= "C:\...\barcodereader.exe" start= auto
const ServiceName = "Barcodereader"

// dispatcherProbeTimeout — сколько ждём ответа SCM, прежде чем решить,
// что процесс запущен как служба.
//
// StartServiceCtrlDispatcher при запуске из консоли возвращает ошибку
// почти мгновенно, а при запуске службой — подвисает до конца работы
// службы. Один этот вызов и есть авторитетный ответ на вопрос «меня
// запустила система или пользователь».
const dispatcherProbeTimeout = 500 * time.Millisecond

// failedServiceControllerConnect — ERROR_FAILED_SERVICE_CONTROLLER_CONNECT:
// именно её Windows возвращает процессу, который запустили не через SCM.
const failedServiceControllerConnect = syscall.Errno(1063)

// RunService запускает сервис как службу Windows или как консольное
// приложение.
//
// Отдельного ключа запуска не требуется: бинарник сам определяет режим по
// тому, подключился ли он к диспетчеру служб.
func RunService(cfg config.Config, logger *slog.Logger) error {
	handler := newServiceHandler(cfg, logger)

	connected, wait, err := probeDispatcher(handler)
	if err != nil {
		// Не сорвалось — считаем себя консольным приложением и пишем
		// предупреждение в тот же журнал.
		logger.Warn("не удалось определить режим запуска, работаю как обычное приложение", "err", err)
		return RunWithSignals(cfg, logger)
	}

	if !connected {
		return RunWithSignals(cfg, logger)
	}

	// Под службой stdout не читает никто: переводим журнал в файл.
	// Само переключение безопасно — запросы ещё не могли прийти.
	if logPath := LogFilePath(cfg.LogFile, config.DefaultLogDirPath()); logPath != "" {
		fileLogger, closer, lerr := NewLogger(cfg.LogLevel, logPath)
		if lerr != nil {
			logger.Warn("журнал в файл недоступен, остаюсь на прежнем", "err", lerr)
		} else {
			// Closer живёт до конца службы: закрывать файл раньше нельзя,
			// а забыть закрыть — значит оставить дескриптор на годы.
			defer closer.Close()
			handler.setLogger(fileLogger)
			logger = fileLogger
			logger.Info("служба запущена под диспетчером служб", "name", ServiceName, "log_file", logPath)
		}
	}

	if err := wait(); err != nil {
		logger.Error("служба остановлена с ошибкой", "err", err)
		return err
	}
	return nil
}

// probeDispatcher пытается подключиться к SCM.
//
// Возвращает признак подключения и функцию ожидания: если подключение
// состоялось, wait() блокируется до конца работы службы и возвращает её
// ошибку.
func probeDispatcher(handler *serviceHandler) (connected bool, wait func() error, err error) {
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ServiceName, handler)
	}()

	select {
	case runErr := <-errCh:
		// Вызов вернулся сразу — значит, SCM нас не ждал.
		if isNotServiceError(runErr) {
			return false, nil, nil
		}
		return false, nil, runErr
	case <-time.After(dispatcherProbeTimeout):
		// Вызов всё ещё работает: подключение состоялось, дальше блокируем.
		return true, func() error { return <-errCh }, nil
	}
}

// isNotServiceError отличает «запущено не как служба» от настоящих ошибок.
func isNotServiceError(err error) bool {
	return errors.Is(err, failedServiceControllerConnect)
}

// serviceHandler реализует svc.Handler: получает команды SCM и переводит их
// в отмену контекста, на который подписан HTTP-сервер.
type serviceHandler struct {
	cfg config.Config

	// start запускает сам сервис. Поле, а не прямой вызов Run, чтобы
	// состояние службы можно было проверить в тестах без подъёма пула
	// PDFium и компиляции wasm.
	start func(context.Context, config.Config, *slog.Logger) error

	// mu защищает logger: сам SCM вызывает Execute уже во время пробного
	// подключения, то есть параллельно с переключением журнала на файл.
	mu     sync.Mutex
	logger *slog.Logger
}

func newServiceHandler(cfg config.Config, logger *slog.Logger) *serviceHandler {
	return &serviceHandler{cfg: cfg, logger: logger, start: Run}
}

func (h *serviceHandler) setLogger(logger *slog.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logger = logger
}

func (h *serviceHandler) getLogger() *slog.Logger {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logger
}

// accepted — команды SCM, на которые служба умеет отвечать.
const accepted = svc.AcceptStop | svc.AcceptShutdown

// status — ответ SCM о состоянии службы.
//
// WaitHint задаётся в миллисекундах: столько SCM готов ждать, прежде
// чем решить, что служба зависла, и убить процесс. Для StopPending там
// запас на завершение текущих запросов.
func status(state svc.State, waitHint time.Duration) svc.Status {
	return svc.Status{
		State:     state,
		Accepts:   accepted,
		WaitHint:  uint32(waitHint / time.Millisecond),
		ProcessId: uint32(currentPID()),
	}
}

// Execute — единственный метод svc.Handler. Он вызывается один раз за время
// жизни службы и обязан вернуться, когда служба останавливается.
func (h *serviceHandler) Execute(
	args []string,
	requests <-chan svc.ChangeRequest,
	changes chan<- svc.Status,
) (svcSpecificEC bool, exitCode uint32) {
	logger := h.getLogger()
	logger.Info("служба запущена", "name", ServiceName, "args", args)

	// StartPending: SCM ждёт, пока служба поднимется.
	changes <- status(svc.StartPending, 60*time.Second)

	ctx, cancel := newServiceContext()
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.start(ctx, h.cfg, logger)
	}()

	changes <- status(svc.Running, 0)

	for {
		select {
		case ctl, ok := <-requests:
			if !ok {
				// SCM закрыл канал — команду остановки придётся ждать
				// другим способом, но инициативу терять нельзя.
				logger.Warn("канал команд SCM закрыт, останавливаюсь")
				cancel()
				// Ошибку закрытия канала прятать нельзя: если сервис к
				// этому моменту уже упал, SCM должен получить ненулевой
				// код, иначе политика перезапуска не сработает.
				err := <-errCh
				changes <- status(svc.Stopped, 0)
				if err != nil {
					logger.Error("сервис остановился с ошибкой", "err", err)
					return false, 1
				}
				return false, 0
			}

			switch ctl.Cmd {
			case svc.Interrogate:
				// Ответ на запрос состояния: ничего не меняем.
				changes <- ctl.CurrentStatus
			case svc.Stop, svc.Shutdown:
				logger.Info("получена команда остановки от SCM", "cmd", ctl.Cmd)

				// StopPending до отмены контекста: текущие запросы должны
				// успеть завершиться, иначе SCM убьёт процесс по таймауту.
				changes <- status(svc.StopPending, ShutdownTimeout+5*time.Second)

				cancel()
				if err := <-errCh; err != nil {
					logger.Error("сервис остановился с ошибкой", "err", err)
				}
				changes <- status(svc.Stopped, 0)
				logger.Info("служба остановлена")
				return false, 0
			default:
				logger.Info("проигнорирована команда SCM", "cmd", ctl.Cmd)
			}
		case err := <-errCh:
			// Служба упала сама: отдаём SCM ненулевой код, чтобы он
			// применил политику перезапуска, заданную при установке.
			if err != nil {
				logger.Error("сервис остановился с ошибкой", "err", err)
				changes <- status(svc.Stopped, 0)
				return false, 1
			}
			changes <- status(svc.Stopped, 0)
			return false, 0
		}
	}
}