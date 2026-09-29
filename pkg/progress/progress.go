package progress

import (
	"errors"
	"time"
)

type Log interface {
	Say(message string)

	Warn(message string)

	Error(message string)

	Detail(message string)

	Debug(line string)

	Span(name string, start, end time.Time, err error, attrs ...Attr)
}

type discarded struct{}

func Discard() Log { return discarded{} }

func (discarded) Say(string) {}

func (discarded) Warn(string) {}

func (discarded) Error(string) {}

func (discarded) Detail(string) {}

func (discarded) Debug(string) {}

func (discarded) Span(string, time.Time, time.Time, error, ...Attr) {}

type Warning struct{ Cause error }

func (w Warning) Error() string { return w.Cause.Error() }

func (w Warning) Unwrap() error { return w.Cause }

func MarkWarning(err error) error {
	if err == nil {
		return nil
	}
	return Warning{Cause: err}
}

func ReportWarning(log Log, err error) error {
	var warning Warning
	if errors.As(err, &warning) {
		log.Warn(warning.Error())
		return nil
	}
	return err
}
