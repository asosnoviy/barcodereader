package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NewLogger создаёт журнал в формате JSON.
//
// Путь берётся из параметра path: пустой означает stdout — так ведёт себя
// консольный запуск и контейнер. Служба Windows запускается без консоли,
// поэтому её stdout никто не читает и журнал ушёл бы в никуда; для неё
// путь всегда задан.
//
// Вместе с журналом возвращается closer: открытый файл держит дескриптор,
// а служба живёт месяцами, и незакрытый файл — это утечка, которую
// заметят только в Windows.
func NewLogger(levelName, path string) (*slog.Logger, io.Closer, error) {
	var writer io.Writer = os.Stdout
	closer := io.Closer(nopCloser{})

	if strings.TrimSpace(path) != "" {
		file, err := openLogFile(path)
		if err != nil {
			return nil, nil, err
		}
		writer, closer = file, file
	}

	logger := slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: parseLevel(levelName)}))
	return logger, closer, nil
}

// nopCloser заглушка для stdout, который закрывать не нужно.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// openLogFile открывает журнал на запись, создавая каталог при необходимости.
//
// ОAppend-режиме решает практическая причина: служба перезапускается при
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

func parseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
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