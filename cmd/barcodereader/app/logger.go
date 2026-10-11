package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logBufferSize — сколько первых записей переносится в файл при
// перенаправлении журнала.
//
// Записи, сделанные до перенаправления, уходят в stdout: под службой их
// никто не прочитает. Поэтому первые несколько — старт службы, инициализация
// пула — копятся в памяти и дописываются в файл вместе с ним.
const logBufferSize = 32

// Logger — журнал, который можно перенаправить с консоли в файл уже после
// создания.
//
// Перенаправление меняет приёмник, а не объект журнала. Это существенно:
// обработчик службы получает журнал до того, как стало известно, что мы
// работаем под SCM, и держит на него ссылку. Если бы вместо приёмника
// подменялся сам *slog.Logger, все, кто уже взял ссылку, продолжили бы
// писать в stdout — то есть в никуда.
type Logger struct {
	*slog.Logger

	mu sync.Mutex
	// file — приёмник после перенаправления; nil означает stdout.
	file *os.File
	// recent — первые записи, предназначенные для переноса в файл.
	recent   []string
	recentN  int
	closeErr error
}

// New создаёт журнал в формате JSON, пока пишущий в stdout.
func New(levelName string) *Logger {
	l := &Logger{recent: make([]string, logBufferSize)}
	l.Logger = slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: parseLevel(levelName)}))
	return l
}

// Write — приёмник для обработчика slog.
func (l *Logger) Write(p []byte) (int, error) {
	l.mu.Lock()

	if l.file != nil {
		file := l.file
		l.mu.Unlock()
		return file.Write(p)
	}

	if l.recentN < len(l.recent) {
		l.recent[l.recentN] = string(p)
		l.recentN++
	}
	l.mu.Unlock()

	return os.Stdout.Write(p)
}

// RedirectTo перенаправляет журнал в файл.
//
// Записи, сделанные до перенаправления, переносятся в файл: без них в
// журнале службы не было бы видно самого начала её работы.
func (l *Logger) RedirectTo(path string) error {
	file, err := openLogFile(path)
	if err != nil {
		return err
	}

	l.mu.Lock()
	pending := make([]string, l.recentN)
	copy(pending, l.recent[:l.recentN])
	dst := l.file
	l.mu.Unlock()

	// Старый файл закрываем только после того, как новый принял записи:
	// при переключении поверх работающего журнала это единственный шанс
	// ничего не потерять.
	for _, entry := range pending {
		if _, err := file.WriteString(entry); err != nil {
			_ = file.Close()
			return err
		}
	}

	l.mu.Lock()
	l.file = file
	l.mu.Unlock()

	if dst != nil {
		_ = dst.Close()
	}
	return nil
}

// Target возвращает текущий приёмник журнала — для диагностики.
func (l *Logger) Target() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return "stdout"
	}
	return l.file.Name()
}

// Close освобождает файл журнала, если он был открыт.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}
	if l.closeErr != nil {
		return l.closeErr
	}
	l.closeErr = l.file.Close()
	l.file = nil
	return l.closeErr
}

// NewLogger создаёт журнал в указанном файле.
//
// Оставлен для мест, где режим запуска известен заранее. Под службой
// удобнее New + RedirectTo: момент, когда файл понадобится, становится
// известен только после подключения к SCM.
func NewLogger(levelName, path string) (*slog.Logger, io.Closer, error) {
	if strings.TrimSpace(path) == "" {
		logger := New(levelName)
		return logger.Logger, logger, nil
	}

	file, err := openLogFile(path)
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: parseLevel(levelName)}))
	return logger, file, nil
}

// openLogFile открывает журнал на запись, создавая каталог при необходимости.
//
// В режиме append решает практическую причина: служба перезапускается при
// падении, и без append предыдущий журнал затирался бы — а именно в нём
// видно, почему служба упала.
func openLogFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("создание каталога журнала %s: %w", dir, err)
		}
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("открытие журнала %s: %w", path, err)
	}
	return file, nil
}

// parseLevel переводит имя уровня в slog.Level.
//
// По умолчанию здесь warn, а не info. Уровень по умолчанию должен
// оставлять в журнале события жизненного цикла и настоящие ошибки, но не
// каждое сканирование: под службой журнал иначе растёт на десятки записей
// в минуту и перестаёт что-то значить. Подробности каждого запроса доступны
// на уровне info или debug, который включается через LOG_LEVEL.
func parseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}

// LogFilePath решает, куда писать журнал службы.
//
// Пустое значение настройки означает «вычислить по умолчанию», а не
// stdout: под службой stdout недоступен, и молча ушедшие в никуда записи
// — худший вариант при разборе инцидента.
func LogFilePath(configured, defaultDir string) string {
	if path := strings.TrimSpace(configured); path != "" {
		return path
	}
	if dir := strings.TrimSpace(defaultDir); dir != "" {
		return filepath.Join(dir, "barcodereader.log")
	}
	return ""
}

// LogFileNameFor датированное имя файла, чтобы не разрастался один журнал:
// barcodereader-2026-10-10.log.
func LogFileNameFor(t time.Time) string {
	return fmt.Sprintf("barcodereader-%s.log", t.Format("2006-01-02"))
}