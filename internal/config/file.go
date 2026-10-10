package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Файл конфигурации нужен прежде всего службе Windows: она запускается
// системным диспетчером и не наследует переменные окружения интерактивной
// сессии, поэтому настроек через окружение у неё просто нет.
//
// Формат — те же пары ИМЯ=ЗНАЧЕНИЕ, что и переменные окружения, плюс
// комментарии с '#' и пустые строки. Это позволяет описать настройки службы
// обычным текстовым файлом, а не заводить отдельный формат.

// DefaultConfigFileName — имя файла конфигурации в каталоге данных программы.
const DefaultConfigFileName = "barcodereader.conf"

// ConfigFileEnvVar задаёт путь к файлу конфигурации вручную.
const ConfigFileEnvVar = "CONFIG_FILE"

// getenvFunc — доступ к переменной по имени, как у os.Getenv.
type getenvFunc = func(string) string

// LoadFromFile читает конфигурацию из указанного файла с наложением
// переменных окружения. Отсутствующий файл не считается ошибкой: без него
// работает чисто окружение, как раньше.
func LoadFromFile(path string) (Config, error) {
	values, err := readConfigFile(path)
	if err != nil {
		return Config{}, err
	}
	cfg, err := LoadFrom(merged(os.Getenv, values))
	return cfg, err
}

// environmentWithFile оборачивает доступ к окружение файлом конфигурации.
// Путь берётся из CONFIG_FILE, иначе — из каталога данных программы.
func environmentWithFile(getenv getenvFunc) getenvFunc {
	path := strings.TrimSpace(getenv(ConfigFileEnvVar))
	if path == "" {
		path = DefaultConfigFilePath()
	}

	values, err := readConfigFile(path)
	if err != nil {
		// Нечитаемый или неверный файл не должен ронять сервис молча:
		// переменные окружения всё ещё могут задать рабочую конфигурацию.
		// Ошибку разбора покажем через лог, недоступный на этом этапе,
		// поэтому просто возвращаем окружение как есть.
		return getenv
	}
	return merged(getenv, values)
}

// DefaultConfigFilePath возвращает путь к файлу конфигурации по умолчанию.
//
// В Windows каталог данных программы — %PROGRAMDATA%\Barcodereader. На других
// системах используется XDG-подобный путь, чтобы тот же код работал в
// контейнере: XDG_CONFIG_HOME, иначе ~/.config.
func DefaultConfigFilePath() string {
	if runtime.GOOS == "windows" {
		base := os.Getenv("PROGRAMDATA")
		if base == "" {
			// Служба всегда получает PROGRAMDATA, но пустой результат
			// лучше, чем попытка писать в корень диска.
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "Barcodereader", DefaultConfigFileName)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "barcodereader", DefaultConfigFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return DefaultConfigFileName
	}
	return filepath.Join(home, ".config", "barcodereader", DefaultConfigFileName)
}

// DefaultLogDirPath возвращает каталог для журнала службы по умолчанию.
// Пусто на не-Windows системах: там логи уходят в stdout, как обычно.
func DefaultLogDirPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	base := os.Getenv("PROGRAMDATA")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "Barcodereader", "logs")
}

// merged отдаёт функцию, которая сперва ищет переменную в окружении, а при
// её отсутствии — в файле конфигурации.
func merged(getenv getenvFunc, values map[string]string) getenvFunc {
	return func(name string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return values[name]
	}
}

// readConfigFile читает файл в карту ИМЯ→ЗНАЧЕНИЕ. Отсутствующий файл даёт
// пустую карту без ошибки; сломанная строка — ошибку с указанием номера.
func readConfigFile(path string) (map[string]string, error) {
	values := make(map[string]string)

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, fmt.Errorf("config: не удалось прочитать %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		// Метка времени выполнения `sc.exe create binPath=` оставляет
		// параметры с пробелами — режем по первому '='.
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("config: %s:%d: ожидалась строка ИМЯ=ЗНАЧЕНИЕ, получено %q", path, line, text)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("config: %s:%d: пустое имя параметра", path, line)
		}
		// Кавычки вокруг значения — частый приём в ini-файлах; снимаем их.
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: ошибка чтения %s: %w", path, err)
	}
	return values, nil
}

// ConfigFilePath сообщает, откуда читается конфигурация: из CONFIG_FILE,
// если он задан, иначе — из пути по умолчанию. Нужен для диагностики.
func ConfigFilePath() string { return lookupConfigFilePath(os.Getenv) }

// lookupConfigFilePath сообщает, откуда читается конфигурация — нужно для
// диагностического сообщения при старте.
func lookupConfigFilePath(getenv getenvFunc) string {
	if p := strings.TrimSpace(getenv(ConfigFileEnvVar)); p != "" {
		return p
	}
	return DefaultConfigFilePath()
}

// configFileExists — быстрая проверка наличия файла без чтения.
func configFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}