package edge

import (
	"errors"
	"time"
)

type Reporter interface {
	Say(message string)

	Detail(message string)

	Span(name string, start, end time.Time, err error, attrs ...Attr)
}

type Attr struct {
	Key   string
	Value string
}

type discarded struct{}

func DiscardReporter() Reporter { return discarded{} }

func (discarded) Say(string) {}

func (discarded) Detail(string) {}

func (discarded) Span(string, time.Time, time.Time, error, ...Attr) {}

type Warning struct{ Cause error }

func (w Warning) Error() string { return w.Cause.Error() }

func (w Warning) Unwrap() error { return w.Cause }

func Warned(err error) error {
	if err == nil {
		return nil
	}
	return Warning{Cause: err}
}

func Heeded(err error, report Reporter) error {
	var warned Warning
	if errors.As(err, &warned) {
		report.Say(warned.Error())
		return nil
	}
	return err
}
