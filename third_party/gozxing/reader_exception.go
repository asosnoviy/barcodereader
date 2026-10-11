package gozxing

import (
	"fmt"

	errors "golang.org/x/xerrors"
)

type ReaderException interface {
	error
	readerException()
}

type readerException struct {
	exception
}

func WrapReaderException(e error) ReaderException {
	return readerException{
		wrapException("ReaderException", e),
	}
}

func (readerException) readerException() {}

type exception struct {
	msg   string
	next  error
	frame errors.Frame
}

// ИСКЛЮЧЕНИЕ В ФОРКЕ barcodereader.
//
// Оригинал снимал кадр стека через xerrors.Caller на каждом создании
// исключения. Линейные ридеры создают NotFoundException на каждой
// неудачной строке страницы: на замере одного вызова это 20 процентов
// всего времени запроса (профилировщик: gozxing.NewNotFoundException →
// xerrors.Caller → runtime.Callers). Сообщение об ошибке при этом
// никто не читает — вызывающий код проверяет только факт ошибки.
//
// Кадр убран, само сообщение и цепочка Unwrap не тронуты: поведение
// декодирования не меняется ни на один байт. Форматирование через %+v
// по-прежнему компилируется, просто не печатает кадр.
//
// Возвращать кадр смысла нет и по другой причине: трассировка внутри
// wasm-стека всё равно указывала бы на внутренности ZXing, а не на
// место в коде сервиса.
func newException(prefix string, args ...interface{}) exception {
	msg := prefix
	if len(args) > 0 {
		msg += ": " + fmt.Sprintf(args[0].(string), args[1:]...)
	}
	return exception{
		msg,
		nil,
		errors.Frame{},
	}
}

func wrapException(msg string, next error) exception {
	return exception{
		msg,
		next,
		errors.Frame{},
	}
}

func (e exception) Error() string {
	return e.msg
}

func (e exception) Unwrap() error {
	return e.next
}

func (e exception) Format(s fmt.State, v rune) {
	errors.FormatError(e, s, v)
}

func (e exception) FormatError(p errors.Printer) error {
	p.Print(e.msg)
	e.frame.Format(p)
	return e.next
}
