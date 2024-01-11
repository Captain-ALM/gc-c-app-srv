package web

import (
	"net/http"
	"strconv"
)

func WriteResponseHeaderCanWriteBody(method string, rw http.ResponseWriter, statusCode int, message string) bool {
	hasBody := method != http.MethodHead && method != http.MethodOptions
	if hasBody && message != "" {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Content-Length", strconv.Itoa(len(message)+2))
		rw.Header().Set("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
		rw.Header().Set("Pragma", "no-cache")
	}
	rw.WriteHeader(statusCode)
	if hasBody {
		if message != "" {
			_, _ = rw.Write([]byte(message + "\r\n"))
			return false
		}
		return true
	}
	return false
}
