package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "barcodereader.conf")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("запись файла: %v", err)
	}
	return path
}

func TestReadConfigFileParsesPairs(t *testing.T) {
	path := writeConf(t, `
# настройки службы
ADDR = :9000

RENDER_DPI=300
LOG_LEVEL="debug"
MAX_PAGES = 3
`)

	values, err := readConfigFile(path)
	if err != nil {
		t.Fatalf("readConfigFile: %v", err)
	}

	cases := map[string]string{
		"ADDR":      ":9000",
		"RENDER_DPI": "300",
		"LOG_LEVEL":  "debug",
		"MAX_PAGES":  "3",
	}
	for name, want := range cases {
		if got := values[name]; got != want {
			t.Errorf("%s = %q, ожидалось %q", name, got, want)
		}
	}
}

func TestReadConfigFileMissingIsNotError(t *testing.T) {
	values, err := readConfigFile(filepath.Join(t.TempDir(), "нет-такого.conf"))
	if err != nil {
		t.Fatalf("отсутствующий файл не должен быть ошибкой: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("ожидалась пустая карта, получено %d значений", len(values))
	}
}

func TestReadConfigFileRejectsMalformedLine(t *testing.T) {
	path := writeConf(t, "ADDR=:9000\nэто_не_пара\n")

	if _, err := readConfigFile(path); err == nil {
		t.Fatal("ожидалась ошибка на строке без '='")
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	// Переменная окружения должна побеждать значение из файла — так
	// сохраняется привычное поведение запуска через docker-compose.
	fileValues := map[string]string{"RENDER_DPI": "300", "MAX_PAGES": "3"}
	env := map[string]string{"RENDER_DPI": "150"}

	cfg, err := LoadFrom(merged(func(k string) string { return env[k] }, fileValues))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.RenderDPI != 150 {
		t.Errorf("RenderDPI = %d, ожидалось 150 (переменная окружения важнее файла)", cfg.RenderDPI)
	}
	if cfg.MaxPages != 3 {
		t.Errorf("MaxPages = %d, ожидалось 3 (значение из файла)", cfg.MaxPages)
	}
}

func TestFileUsedWhenEnvAbsent(t *testing.T) {
	fileValues := map[string]string{"ADDR": ":9001", "RENDER_DPI": "300"}

	cfg, err := LoadFrom(merged(func(string) string { return "" }, fileValues))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Addr != ":9001" {
		t.Errorf("Addr = %q, ожидалось \":9001\"", cfg.Addr)
	}
	if cfg.RenderDPI != 300 {
		t.Errorf("RenderDPI = %d, ожидалось 300", cfg.RenderDPI)
	}
}

// Пустое значение переменной окружения не должно затирать файл: в
// docker-compose строка `ADDR=` встречается чаще, чем кажется.
func TestEmptyEnvDoesNotOverrideFile(t *testing.T) {
	fileValues := map[string]string{"ADDR": ":9001"}
	env := map[string]string{"ADDR": ""}

	cfg, err := LoadFrom(merged(func(k string) string { return env[k] }, fileValues))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Addr != ":9001" {
		t.Errorf("Addr = %q, ожидалось \":9001\" из файла", cfg.Addr)
	}
}

func TestLogFileSetting(t *testing.T) {
	cfg, err := LoadFrom(func(k string) string {
		if k == "LOG_FILE" {
			return `C:\ProgramData\Barcodereader\logs\service.log`
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.LogFile == "" {
		t.Error("LOG_FILE должен попадать в конфигурацию")
	}
}

func TestDefaultConfigFilePathIsAbsolute(t *testing.T) {
	path := DefaultConfigFilePath()
	if path == "" {
		t.Fatal("путь конфигурации по умолчанию не должен быть пустым")
	}
	if !filepath.IsAbs(path) && path != DefaultConfigFileName {
		t.Errorf("ожидался абсолютный путь, получено %q", path)
	}
}

func TestConfigFileEnvVarSelectsPath(t *testing.T) {
	path := writeConf(t, "ADDR=:9999\n")
	t.Setenv(ConfigFileEnvVar, path)

	if got := lookupConfigFilePath(os.Getenv); got != path {
		t.Errorf("путь из %s не подхвачен: %q", ConfigFileEnvVar, got)
	}

	// Переменная окружения по-прежнему важнее файла.
	t.Setenv("ADDR", ":7777")
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	if cfg.Addr != ":7777" {
		t.Errorf("Addr = %q, ожидалось \":7777\" из окружения", cfg.Addr)
	}
}

func TestLoadFromFileRejectsMalformed(t *testing.T) {
	path := writeConf(t, "ADDR=:9999\nсломанная_строка\n")

	if _, err := LoadFromFile(path); err == nil {
		t.Fatal("ожидалась ошибка разбора файла")
	}
}

func TestConfigFileExists(t *testing.T) {
	path := writeConf(t, "ADDR=:9000\n")
	if !configFileExists(path) {
		t.Error("существующий файл должен определяться")
	}
	if configFileExists(filepath.Join(t.TempDir(), "нет.conf")) {
		t.Error("отсутствующий файл не должен определяться")
	}
	if configFileExists(t.TempDir()) {
		t.Error("каталог не должен считаться файлом конфигурации")
	}
}