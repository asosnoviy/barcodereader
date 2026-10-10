package app

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogFilePathUsesConfiguredValue(t *testing.T) {
	got := LogFilePath(`C:\custom\my.log`, `C:\ProgramData\Barcodereader\logs`)
	if got != `C:\custom\my.log` {
		t.Errorf("LogFilePath = %q, ожидался заданный путь", got)
	}
}

func TestLogFilePathFallsBackToDefaultDir(t *testing.T) {
	got := LogFilePath("", `C:\ProgramData\Barcodereader\logs`)
	want := filepath.Join(`C:\ProgramData\Barcodereader\logs`, "barcodereader.log")
	if got != want {
		t.Errorf("LogFilePath = %q, ожидалось %q", got, want)
	}
}

// На не-Windows системах каталога по умолчанию нет: пустой путь означает
// stdout, иначе контейнерный запуск вдруг начнёт писать в файл.
func TestLogFilePathEmptyWithoutDefaultDir(t *testing.T) {
	if got := LogFilePath("", ""); got != "" {
		t.Errorf("LogFilePath = %q, ожидалась пустая строка (stdout)", got)
	}
	if got := LogFilePath("   ", "  "); got != "" {
		t.Errorf("пробелы должны считаться отсутствием значения, получено %q", got)
	}
}

func TestNewLoggerWritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "service.log")

	logger, closer, err := NewLogger("info", path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closer.Close()
	logger.Info("служба запущена", "addr", ":8080")

	// Запись в файл синхронная, но на Windows файл может быть ещё не
	// виден читателю, пока дескриптор не отдан ОС.
	data := readFileEventually(t, path)
	if !strings.Contains(data, "служба запущена") {
		t.Errorf("в журнале нет записи: %q", data)
	}
	if !strings.Contains(data, `"addr":":8080"`) {
		t.Errorf("в журнале нет атрибутов: %q", data)
	}
}

// При перезапуске службы журнал должен дополняться, а не затираться:
// именно в нём видно, почему служба упала в прошлый раз.
func TestNewLoggerAppendsToExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("прежняя запись\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	logger, closer, err := NewLogger("info", path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closer.Close()
	logger.Info("новая запись")

	data := readFileEventually(t, path)
	if !strings.Contains(data, "прежняя запись") || !strings.Contains(data, "новая запись") {
		t.Errorf("журнал не дополнен, а перезаписан: %q", data)
	}
}

// Каталог журнала создаётся автоматически: служба запускается без
// установки, и требовать заранее подготовленного каталога нельзя.
func TestNewLoggerCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "глубоко", "вложенный", "путь")
	path := filepath.Join(dir, "service.log")

	logger, closer, err := NewLogger("info", path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	defer closer.Close()
	logger.Info("проверка")

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("каталог журнала не создан: %v", err)
	}
	if data := readFileEventually(t, path); !strings.Contains(data, "проверка") {
		t.Errorf("в журнале нет записи: %q", data)
	}
}

// Путь, который нельзя создать, должен давать ошибку, а не молча
// подменять журнал: молчание превратилось бы в потерянные записи.
func TestNewLoggerFailsOnInvalidPath(t *testing.T) {
	// Заведомо недопустимое имя файла: нулевой символ недопустим в Windows.
	if _, _, err := NewLogger("info", "недопустимое\x00имя.log"); err == nil {
		t.Error("ожидалась ошибка открытия файла с недопустимым именем")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"мусор": slog.LevelInfo,
	}
	for name, want := range cases {
		if got := parseLevel(name); got != want {
			t.Errorf("parseLevel(%q) = %v, ожидалось %v", name, got, want)
		}
	}
}

func TestLogFileNameFor(t *testing.T) {
	got := LogFileNameFor(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	if got != "barcodereader-2026-10-10.log" {
		t.Errorf("LogFileNameFor = %q", got)
	}
}

// readFileEventually читает файл с небольшими повторами: запись могла ещё не
// дойти до диска, и это не повод валить тест.
func readFileEventually(t *testing.T, path string) string {
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