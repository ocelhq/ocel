package runtrace

import (
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func (r *Run) Receive(ev *streamv1.RunEvent) {
	r.record(ev)
	switch {
	case ev.GetStarted() != nil:
		r.remember(ev)
	case ev.GetEnded() != nil:
		r.ingestEnded(ev)
	}
}

func (r *Run) record(ev *streamv1.RunEvent) {
	raw, err := protojson.Marshal(persisted(ev))
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	r.logMu.Lock()
	defer r.logMu.Unlock()
	_, _ = r.logFile.Write(raw)
}

func persisted(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev = proto.CloneOf(ev)
	if waiting := ev.GetWaiting(); waiting != nil {
		waiting.Url, _, _ = strings.Cut(waiting.GetUrl(), "#")
	}
	redact(ev.ProtoReflect())
	return ev
}

func redact(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if opts, ok := fd.Options().(*descriptorpb.FieldOptions); ok && opts.GetDebugRedact() {
			m.Clear(fd)
			return true
		}
		switch {
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, entry protoreflect.Value) bool {
					redact(entry.Message())
					return true
				})
			}
		case fd.Message() == nil:
		case fd.IsList():
			for i := range v.List().Len() {
				redact(v.List().Get(i).Message())
			}
		default:
			redact(v.Message())
		}
		return true
	})
}
