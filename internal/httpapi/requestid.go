package httpapi

import (
	"crypto/rand"
	"encoding/hex"
)

// newRequestID возвращает короткий идентификатор запроса для логов и ответа.
// Он не является идентификатором корреляции в распределённом смысле: цель —
// связать строку лога с телом ответа.
func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(buf[:])
}