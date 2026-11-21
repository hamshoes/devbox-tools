// Package tetherapp is what a pair app links against: a client for calling its
// tether-gw over a unix socket, and a server for the calls the gateway forwards
// to it. The wire protocol is a subset of Connect unary: POST /<Service>/<Method>
// with a protojson body, answered by protojson or {"code","message"}.
package tetherapp

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Connect error codes used by the gateway protocol.
const (
	CodeInvalidArgument  = "invalid_argument"
	CodePermissionDenied = "permission_denied"
	CodeUnimplemented    = "unimplemented"
	CodeUnavailable      = "unavailable"
	CodeDeadlineExceeded = "deadline_exceeded"
	CodeInternal         = "internal"
	CodeCanceled         = "canceled"
)

// MaxBody is the largest request or response body, in bytes.
const MaxBody = 1 << 20

// Error is a failed call, as returned by Call and accepted from handlers.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("tetherapp: %s: %s", e.Code, e.Message) }

// Errorf builds an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// HTTPStatus is the status a code travels with. Unknown codes are 500.
func HTTPStatus(code string) int {
	switch code {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeUnimplemented:
		return http.StatusNotFound
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case CodeCanceled:
		return 499
	}
	return http.StatusInternalServerError
}

// WriteError sends e as the error response.
func WriteError(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(HTTPStatus(e.Code))
	json.NewEncoder(w).Encode(e)
}
