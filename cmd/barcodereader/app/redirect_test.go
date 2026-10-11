package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Регрессия: обработчик службы получает журнал ДО того, как стало известно,
// что процесс работает под SCM. Если в этот момент подменить сам журнал,
// а не приёмник, все позаимствованные ссылки продолжат писать в stdout —
// то есть под службой, где stdout не читает никто, записи просто пропадают.
// Именно это наблюдалось: в файле журнала службы была только строка о
// подключении, а ни одного сообщения об ошибке ни при каких обстоятельствах.
func TestRedirectKeepsAlreadyHeldLoggerReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.log")

	logger := New("info")
	// Ссылка берётся ДО перенаправления — так же, как её берёт SCM.
	held := logger
	held.Info("до перенаправления")

	if err := logger.RedirectTo(path); err != nil {
		t.Fatalf("RedirectTo: %v", err)
	}
	defer logger.Close()

	// Пишем через ту же ссылку, а не через logger.
	held.Info("после перенаправления")

	data := readEventually(t, path)
	if !strings.Contains(data, "после перенаправления") {
		t.Errorf("запись через ранее взятую ссылку не попала в файл:\n%s", data)
	}
}

// Записи, сделанные до перенаправления, должны сохраниться: без них в
// журнале службы не было бы видно начала её работы.
func TestRedirectPreservesEarlierEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.log")

	logger := New("info")
	logger.Info("служба запущена под диспетчером служб")

	if err := logger.RedirectTo(path); err != nil {
		t.Fatalf("RedirectTo: %v", err)
	}
	defer logger.Close()

	data := readEventually(t, path)
	if !strings.Contains(data, "служба запущена под диспетчером служб") {
		t.Errorf("ранняя запись не перенесена в файл:\n%s", data)
	}
}

func TestTargetReportsCurrentSink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.log")

	logger := New("info")
	if got := logger.Target(); got != "stdout" {
		t.Errorf("до перенаправления Target = %q, ожидался stdout", got)
	}
	if err := logger.RedirectTo(path); err != nil {
		t.Fatalf("RedirectTo: %v", err)
	}
	defer logger.Close()
	if got := logger.Target(); got != path {
		t.Errorf("после перенаправления Target = %q, ожидался %q", got, path)
	}
}

func TestCloseIsSafeWithoutFile(t *testing.T) {
	logger := New("info")
	if err := logger.Close(); err != nil {
		t.Errorf("Close без файла: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Errorf("повторный Close: %v", err)
	}
}

func TestRedirectToBadPathKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.log")

	logger := New("info")
	if err := logger.RedirectTo(path); err != nil {
		t.Fatalf("первое перенаправление: %v", err)
	}
	defer logger.Close()

	// Недопустимое имя файла: перенаправление обязано провалиться,
	// а не молча оставить журнал в непредсказуемом состоянии.
	if err := logger.RedirectTo("недопустимое\x00имя.log"); err == nil {
		t.Error("ожидалась ошибка перенаправления по недопустимому пути")
	}
	if got := logger.Target(); got != path {
		t.Errorf("после неудачного перенаправления Target = %q, ожидался прежний %q", got, path)
	}
}

func readEventually(t *testing.T, path string) string {
	t.Helper()

	var data []byte
	var err error
	for range 50 {
		data, err = os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("не удалось прочитать %s: %v", path, err)
	return ""
}