package pdfrender

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"
)

// brokenStdout подменяет os.Stdout файлом с уже закрытым дескриптором —
// именно так выглядит процесс службы Windows, у которого нет консоли.
// Возвращает функцию восстановления.
func brokenStdout(t *testing.T) func() {
	t.Helper()

	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("не удалось открыть %s: %v", os.DevNull, err)
	}
	handle := f.Fd()
	if err := f.Close(); err != nil {
		t.Fatalf("закрытие дескриптора: %v", err)
	}

	saved := os.Stdout
	// Имя повторяет стандартное имя os.Stdout в Go, поэтому текст ошибки
	// совпадает с тем, что приходил из журнала службы.
	os.Stdout = os.NewFile(handle, "/dev/stdout")
	return func() { os.Stdout = saved }
}

// TestNewRendererWithUnusableStdout проверяет, что пул PDFium поднимается без
// рабочих стандартных потоков. Wazero запрашивает Stat() у переданного файла,
// поэтому раньше под службой создание инстанса падало с
// «could not instantiate webassembly module: GetFileType /dev/stdout:
// The handle is invalid» и ни один документ не открывался.
func TestNewRendererWithUnusableStdout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("GetFileType есть только в Windows")
	}
	defer brokenStdout(t)()

	r, err := NewRenderer(1)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	defer r.Close()

	inst, err := r.pool.GetInstanceWithContext(context.Background())
	if err != nil {
		t.Fatalf("инстанс PDFium: %v", err)
	}
	if err := inst.Close(); err != nil {
		t.Fatalf("закрытие инстанса: %v", err)
	}
}

// TestGuestOutputStaysOutOfServiceLog удерживает вывод гостя на уровне DEBUG:
// в журнале службы он не нужен, а на INFO он превратился бы в шум.
func TestGuestOutputStaysOutOfServiceLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	payload := []byte("отладочный вывод PDFium\r\n")
	n, err := guestOutput{}.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write вернул %d, ожидалось %d", n, len(payload))
	}
	if strings.Contains(buf.String(), "вывод модуля PDFium") {
		t.Errorf("вывод гостя попал в журнал уровня INFO: %s", buf.String())
	}
}