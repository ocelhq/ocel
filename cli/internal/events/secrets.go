package events

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func withoutSecrets(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev = proto.CloneOf(ev)
	redact(ev.ProtoReflect())
	return ev
}

func redact(m protoreflect.Message) {
	if custom, ok := m.Interface().(*structpb.Struct); ok {
		for name := range custom.GetFields() {
			custom.Fields[name] = structpb.NewNullValue()
		}
		return
	}
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
