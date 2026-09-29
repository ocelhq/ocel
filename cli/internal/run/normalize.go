package run

import (
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func (b *Bus) stamp(ev *streamv1.RunEvent) {
	if ev.GetTime() == nil {
		ev.Time = timestamppb.New(b.now())
	}
	if ended := ev.GetEnded(); ended != nil && ev.GetTime().AsTime().UnixNano() < ended.GetStartTimeUnixNano() {
		ev.Time = timestamppb.New(b.now())
	}
	if ev.GetLevel() == progressv1.Level_LEVEL_UNSPECIFIED {
		ev.Level = progressv1.Level_LEVEL_INFO
	}
}

func collapse(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			v.Map().Range(func(key protoreflect.MapKey, entry protoreflect.Value) bool {
				collapseValue(fd.MapValue(), entry, func(nv protoreflect.Value) { v.Map().Set(key, nv) })
				return true
			})
		case fd.IsList():
			for i := range v.List().Len() {
				collapseValue(fd, v.List().Get(i), func(nv protoreflect.Value) { v.List().Set(i, nv) })
			}
		default:
			collapseValue(fd, v, func(nv protoreflect.Value) { m.Set(fd, nv) })
		}
		return true
	})
}

func collapseValue(fd protoreflect.FieldDescriptor, v protoreflect.Value, set func(protoreflect.Value)) {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		collapse(v.Message())
	case protoreflect.StringKind:
		if collapsed := collapseRewrites(v.String()); collapsed != v.String() {
			set(protoreflect.ValueOfString(collapsed))
		}
	}
}
