package apperr

import "fmt"

// Error 是接口返回的领域错误。普通 error 一律当成 500。
type Error struct {
	Code   string
	Status int
	Msg    string
}

func (e *Error) Error() string { // error 接口，方便 errors.As。
	return e.Msg
}

// New 构造带 HTTP 状态和错误码的业务错误。
func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Msg: msg}
}

func (e *Error) Format(s fmt.State, verb rune) { // 日志里同时看到 code 和 message。
	fmt.Fprintf(s, "%s: %s", e.Code, e.Msg)
}
