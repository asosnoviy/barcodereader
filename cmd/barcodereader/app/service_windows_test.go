//go:build windows

package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"

	"barcodereader/internal/config"
)

// quietLogger уводит журнал в /dev/null: тесты проверяют состояния SCM,
// а не текст сообщений.
func quietLogger() *Logger {
	log := New("info")
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0o600)
	if err == nil {
		log.file = devNull
	}
	return log
}

// newTestHandler собирает обработчик службы с подменённым запуском, чтобы
// не поднимать настоящий HTTP-сервер и пул PDFium.
func newTestHandler(start func(context.Context, config.Config, *Logger) error) *serviceHandler {
	h := newServiceHandler(config.Default(), quietLogger())
	h.start = start
	return h
}

// executeHandler прогоняет обработчик и собирает все статусы, которые он
// отправил в SCM.
func executeHandler(t *testing.T, h *serviceHandler, commands ...svc.Cmd) ([]svc.Status, bool, uint32) {
	t.Helper()

	requests := make(chan svc.ChangeRequest, len(commands))
	for _, cmd := range commands {
		requests <- svc.ChangeRequest{Cmd: cmd}
	}
	changes := make(chan svc.Status, 16)

	done := make(chan struct{})
	var (
		statuses []svc.Status
		svcSpec  bool
		exitCode uint32
	)
	go func() {
		defer close(done)
		svcSpec, exitCode = h.Execute([]string{"Barcodereader"}, requests, changes)
	}()

	// Канал команд закрываем: обработчик обязан корректно выйти и без
	// явной команды Stop.
	close(requests)

	timeout := time.After(10 * time.Second)
	for {
		select {
		case s := <-changes:
			statuses = append(statuses, s)
		case <-done:
			// Дочитываем остаток статусов из буфера.
			for {
				select {
				case s := <-changes:
					statuses = append(statuses, s)
				default:
					return statuses, svcSpec, exitCode
				}
			}
		case <-timeout:
			t.Fatal("обработчик не завершился за 10 секунд")
		}
	}
}

func states(list []svc.Status) []svc.State {
	out := make([]svc.State, 0, len(list))
	for _, s := range list {
		out = append(out, s.State)
	}
	return out
}

func hasState(list []svc.State, want svc.State) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Служба обязана пройти путь StartPending → Running и завершиться по команде
// Stop, отдав SCM статус Stopped.
func TestServiceHandlerStopCommand(t *testing.T) {
	started := make(chan struct{})
	h := newTestHandler(func(ctx context.Context, _ config.Config, _ *Logger) error {
		close(started)
		<-ctx.Done()
		return nil
	})

	statuses, _, exitCode := executeHandler(t, h, svc.Stop)

	got := states(statuses)
	for _, want := range []svc.State{svc.StartPending, svc.Running, svc.Stopped} {
		if !hasState(got, want) {
			t.Errorf("нет состояния %v в цепочке %v", want, got)
		}
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, ожидался 0 при штатной остановке", exitCode)
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("сервис не был запущен вовсе")
	}
}

// Порядок состояний важен для SCM: StopPending должен предшествовать
// Stopped, иначе диспетчер решит, что служба зависла.
func TestServiceHandlerReportsStopPendingBeforeStopped(t *testing.T) {
	h := newTestHandler(func(ctx context.Context, _ config.Config, _ *Logger) error {
		<-ctx.Done()
		return nil
	})

	statuses, _, _ := executeHandler(t, h, svc.Stop)

	stopPendingAt, stoppedAt := -1, -1
	for i, s := range statuses {
		switch s.State {
		case svc.StopPending:
			stopPendingAt = i
		case svc.Stopped:
			stoppedAt = i
		}
	}
	if stopPendingAt < 0 {
		t.Fatalf("состояние StopPending не отправлено: %v", states(statuses))
	}
	if stoppedAt < 0 {
		t.Fatalf("состояние Stopped не отправлено: %v", states(statuses))
	}
	if stopPendingAt > stoppedAt {
		t.Errorf("StopPending (%d) должен идти раньше Stopped (%d)", stopPendingAt, stoppedAt)
	}
}

// StopPending обязан объявить SCM время ожидания: без WaitHint диспетчер
// считает службу зависшей и убивает процесс по своему таймауту.
func TestServiceHandlerStopPendingHasWaitHint(t *testing.T) {
	h := newTestHandler(func(ctx context.Context, _ config.Config, _ *Logger) error {
		<-ctx.Done()
		return nil
	})

	statuses, _, _ := executeHandler(t, h, svc.Stop)

	for _, s := range statuses {
		if s.State != svc.StopPending {
			continue
		}
		if s.WaitHint == 0 {
			t.Error("WaitHint в StopPending равен нулю: SCM решит, что служба зависла")
		}
		wantMS := uint32((ShutdownTimeout + 5*time.Second) / time.Millisecond)
		if s.WaitHint != wantMS {
			t.Errorf("WaitHint = %d мс, ожидалось %d мс", s.WaitHint, wantMS)
		}
		return
	}
	t.Fatalf("StopPending не отправлен: %v", states(statuses))
}

// Падение сервиса должно быть видно SCM ненулевым кодом: иначе
// настроенная политика перезапуска не сработает.
func TestServiceHandlerCrashReportsNonZeroExit(t *testing.T) {
	h := newTestHandler(func(context.Context, config.Config, *Logger) error {
		return errors.New("упало")
	})

	statuses, _, exitCode := executeHandler(t, h)

	if exitCode == 0 {
		t.Errorf("exitCode = 0 при аварии, ожидался ненулевой код")
	}
	if !hasState(states(statuses), svc.Stopped) {
		t.Errorf("после аварии ожидалось состояние Stopped, получено %v", states(statuses))
	}
}

// Interrogate — запрос состояния: служба обязана ответить, не меняя
// ничего. Случайные команды не должны ронять службу.
func TestServiceHandlerIgnoresUnknownCommands(t *testing.T) {
	h := newTestHandler(func(ctx context.Context, _ config.Config, _ *Logger) error {
		<-ctx.Done()
		return nil
	})

	statuses, _, exitCode := executeHandler(t, h, svc.Interrogate, svc.ParamChange, svc.Stop)

	got := states(statuses)
	if !hasState(got, svc.Running) {
		t.Errorf("служба не дошла до Running: %v", got)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, ожидался 0", exitCode)
	}
}

// Закрытие канала команд SCM не должно оставлять службу висеть.
func TestServiceHandlerExitsWhenCommandChannelCloses(t *testing.T) {
	h := newTestHandler(func(ctx context.Context, _ config.Config, _ *Logger) error {
		<-ctx.Done()
		return nil
	})

	statuses, _, _ := executeHandler(t, h)

	if !hasState(states(statuses), svc.Stopped) {
		t.Errorf("после закрытия канала ожидалось Stopped, получено %v", states(statuses))
	}
}

func TestStatusCarriesPIDAndAccepts(t *testing.T) {
	h := newTestHandler(func(context.Context, config.Config, *Logger) error { return nil })

	statuses, _, _ := executeHandler(t, h)

	for _, s := range statuses {
		if s.State != svc.Running {
			continue
		}
		if s.Accepts&svc.AcceptStop == 0 {
			t.Error("служба не объявила AcceptStop: команда Stop будет отвергнута")
		}
		if s.Accepts&svc.AcceptShutdown == 0 {
			t.Error("служба не объявила AcceptShutdown: выключение Windows её убьёт")
		}
		if s.ProcessId != uint32(currentPID()) {
			t.Errorf("ProcessId = %d, ожидался %d", s.ProcessId, currentPID())
		}
		return
	}
	t.Fatalf("состояние Running не отправлено: %v", states(statuses))
}

func TestIsNotServiceError(t *testing.T) {
	if !isNotServiceError(failedServiceControllerConnect) {
		t.Error("ошибка 1063 должна распознаваться как «запущено не как служба»")
	}
	if isNotServiceError(errors.New("что-то другое")) {
		t.Error("посторонняя ошибка не должна считаться отказом SCM")
	}
}