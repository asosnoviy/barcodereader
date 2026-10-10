// Package apierrors описывает типизированные ошибки сервиса и их отображение
// в HTTP-ответы: машиночитаемый код, текст и HTTP-статус.
package apierrors

import (
	"errors"
	"fmt"
	"net/http"
)

// Error — ошибка, безопасная для возврата клиенту.
type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	Details    map[string]any
	cause      error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap возвращает исходную причину: она попадает только в лог, но не в ответ.
func (e *Error) Unwrap() error { return e.cause }

// WithDetails добавляет технические детали (например, число просканированных страниц).
func (e *Error) WithDetails(details map[string]any) *Error {
	e.Details = details
	return e
}

func newErr(status int, code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status, cause: cause}
}

// Конструкторы для каждого кода из контракта API.

func InvalidJSON(cause error) *Error {
	return newErr(http.StatusBadRequest, "invalid_json", "тело запроса не является корректным JSON", cause)
}

func MissingField(field string) *Error {
	return newErr(http.StatusBadRequest, "missing_field", fmt.Sprintf("отсутствует обязательное поле %q", field), nil).
		WithDetails(map[string]any{"field": field})
}

func InvalidPages(selection string, cause error) *Error {
	return newErr(http.StatusBadRequest, "invalid_pages",
		fmt.Sprintf("некорректная выборка страниц %q, ожидается \"1\", \"1-3\" или \"1,3,5-7\"", selection), cause).
		WithDetails(map[string]any{"pages": selection})
}

func InvalidBase64(cause error) *Error {
	return newErr(http.StatusBadRequest, "invalid_base64", "поле pdf_base64 не является корректным base64", cause)
}

func NotAPDF() *Error {
	return newErr(http.StatusBadRequest, "not_a_pdf", "данные не начинаются с сигнатуры %PDF-", nil)
}

func PayloadTooLarge(limit int64) *Error {
	return newErr(http.StatusRequestEntityTooLarge, "payload_too_large",
		fmt.Sprintf("превышен лимит размера PDF в %d байт", limit), nil).
		WithDetails(map[string]any{"limit_bytes": limit})
}

func UnsupportedMediaType(contentType string) *Error {
	return newErr(http.StatusUnsupportedMediaType, "unsupported_media_type",
		"ожидается Content-Type application/json или application/pdf", nil).
		WithDetails(map[string]any{"content_type": contentType})
}

func PDFEncrypted(cause error) *Error {
	return newErr(http.StatusUnprocessableEntity, "pdf_encrypted",
		"PDF защищён паролем и не может быть обработан", cause)
}

func PDFUnreadable(cause error) *Error {
	return newErr(http.StatusUnprocessableEntity, "pdf_unreadable",
		"PDF не удалось прочитать: файл повреждён или использует неподдерживаемые конструкции", cause)
}

func NoCodesFound(details map[string]any) *Error {
	return newErr(http.StatusNotFound, "no_codes_found",
		"QR-коды и штрихкоды в документе не найдены", nil).WithDetails(details)
}

func TooManyRequests(waited string) *Error {
	return newErr(http.StatusTooManyRequests, "too_many_requests",
		"сервис занят: не удалось получить слот декодирования за "+waited, nil)
}

func NoCapacity(cause error) *Error {
	return newErr(http.StatusServiceUnavailable, "no_capacity",
		"рендерер PDF временно недоступен, попробуйте позже", cause)
}

func Timeout() *Error {
	return newErr(http.StatusGatewayTimeout, "timeout", "превышено время декодирования документа", nil)
}

func ShuttingDown() *Error {
	return newErr(http.StatusServiceUnavailable, "shutting_down", "сервис завершает работу", nil)
}

func Internal(cause error) *Error {
	return newErr(http.StatusInternalServerError, "internal_error", "внутренняя ошибка сервиса", cause)
}

// AsError извлекает *Error из цепочки ошибок; если ошибки нет, возвращается
// обёртка internal_error с исходной причиной для записи в лог.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return Internal(err)
}