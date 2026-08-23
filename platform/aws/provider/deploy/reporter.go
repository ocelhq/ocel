package deploy

import (
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type engineReporter struct {
	say    func(string)
	detail func(string)
	tracer Tracer
	parent StageID
}

func reporterFor(tracer Tracer, parent StageID, say, detail func(string)) providerkit.Reporter {
	return engineReporter{say: say, detail: detail, tracer: tracer, parent: parent}
}

func (r engineReporter) Say(message string) {
	if r.say != nil {
		r.say(sanitizeMessage(message))
	}
}

func (r engineReporter) Detail(message string) {
	if r.detail != nil {
		r.detail(message)
	}
}

func (r engineReporter) Span(name string, start, end time.Time, err error, attrs ...providerkit.Attr) {
	spanUnder(r.tracer, r.parent, name, start, end, err, tracedAttrs(attrs)...)
}

func tracedAttrs(attrs []providerkit.Attr) []Attr {
	traced := make([]Attr, 0, len(attrs))
	for _, attr := range attrs {
		traced = append(traced, Attr{Key: providerkit.AttributeKey(attr.Key), Value: attr.Value})
	}
	return traced
}
