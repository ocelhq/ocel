package providerkit

import (
	"fmt"
	"time"
)

type Reporter interface {
	Say(message string)

	Detail(message string)

	Span(name string, start, end time.Time, err error, attrs ...Attr)
}

type Code string

const (
	CodeInvalid  Code = "invalid"
	CodeNotReady Code = "not-ready"
	CodeDenied   Code = "denied"
	CodeBusy     Code = "busy"
)

type Refusal struct {
	Code    Code
	Message string
}

func (r Refusal) Error() string { return r.Message }

func Refuse(code Code, format string, args ...any) error {
	return Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}
