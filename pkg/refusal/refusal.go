package refusal

import (
	"errors"
	"fmt"
)

type Code string

const (
	CodeInvalid       Code = "invalid"
	CodeNotReady      Code = "not-ready"
	CodeDenied        Code = "denied"
	CodeBusy          Code = "busy"
	CodeUnknownOption Code = "unknown-option"
	CodeUnsupported   Code = "unsupported"
	CodeNeedsRunner   Code = "needs-runner"
)

type Refusal struct {
	Code    Code
	Message string
}

func (r Refusal) Error() string { return r.Message }

func Refuse(code Code, format string, args ...any) error {
	return Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

type Option struct{ Refusal }

func (o Option) Unwrap() error { return o.Refusal }

func RefuseOption(code Code, format string, args ...any) error {
	return Option{Refusal: Refusal{Code: code, Message: fmt.Sprintf(format, args...)}}
}

func IsOption(err error) bool {
	var refused Option
	return errors.As(err, &refused)
}
