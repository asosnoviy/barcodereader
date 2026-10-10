//go:build windows

package app

import (
	"context"
	"os"
)

// Определение режима запуска.
//
// Отдельный вызов IsWindowsService в x/sys нет. Проверять «видима ли
// оконная станция» тоже ненадёжно: на практике запущенная из консоли
// программа попадает в WinSta0 с нулевыми флагами, и такой тест принимает
// обычное приложение за службу.
//
// Поэтому режим определяется по поведению самого SCM: вызов
// StartServiceCtrlDispatcher (внутри svc.Run) у процесса, запущенного
// службой, подвисает до конца её работы, а у любого другого возвращает
// ошибку 1063 почти мгновенно. Это и есть источник истины.

// currentPID — идентификатор процесса для ответа SCM.
func currentPID() int { return os.Getpid() }

// newServiceContext создаёт контекст, отменяемый командой остановки от SCM.
func newServiceContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}