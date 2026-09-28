package apperr

import "fmt"

type Detail struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Error struct {
	Status  int
	Code    string
	Message string
	Details []Detail
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func Invalid(message string) *Error {
	return New(400, "request.validation_failed", message)
}

func JSON() *Error {
	return New(400, "request.invalid_json", "请求体不是合法 JSON")
}

func Unauth(message string) *Error {
	return New(401, "auth.unauthenticated", message)
}

func Forbidden() *Error {
	return New(403, "auth.forbidden", "没有权限执行该操作")
}

func NotFound() *Error {
	return New(404, "resource.not_found", "资源不存在")
}

func Conflict(code, message string) *Error {
	return New(409, code, message)
}

func VersionMismatch() *Error {
	return New(412, "request.version_mismatch", "If-Match 与当前版本不一致")
}

func Wrap(status int, code, message string, details []Detail) *Error {
	return &Error{Status: status, Code: code, Message: message, Details: details}
}

func As(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	ae, ok := err.(*Error)
	if ok {
		return ae, true
	}
	return nil, false
}

func Messagef(status int, code, format string, args ...any) *Error {
	return New(status, code, fmt.Sprintf(format, args...))
}
