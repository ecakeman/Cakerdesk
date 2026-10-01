package apperr

import "fmt"

type Error struct {
	Code   string
	Status int
	Msg    string
}

func (e *Error) Error() string {
	return e.Msg
}

func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Msg: msg}
}

func (e *Error) Format(s fmt.State, verb rune) {
	fmt.Fprintf(s, "%s: %s", e.Code, e.Msg)
}
