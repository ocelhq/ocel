package edge

import "time"

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
