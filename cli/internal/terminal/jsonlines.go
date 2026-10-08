package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type JSONLines struct {
	w io.Writer
}

func NewJSONLines(w io.Writer) *JSONLines {
	return &JSONLines{w: w}
}

func (s *JSONLines) Receive(ev *streamv1.RunEvent) {
	line, err := envelopeJSON(withValidUTF8(ev))
	if err != nil {
		return
	}
	fmt.Fprintln(s.w, line)
}

func (s *JSONLines) Close() error { return nil }

func withValidUTF8(ev *streamv1.RunEvent) *streamv1.RunEvent {
	valid := proto.CloneOf(ev)
	if valid != nil {
		replaceInvalidUTF8(valid.ProtoReflect())
	}
	return valid
}

func replaceInvalidUTF8(m protoreflect.Message) {
	m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsList():
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				list.Set(i, validUTF8Value(field, list.Get(i)))
			}
		case field.IsMap():
			replaceInvalidUTF8InMap(field, value.Map())
		default:
			m.Set(field, validUTF8Value(field, value))
		}
		return true
	})
}

func replaceInvalidUTF8InMap(field protoreflect.FieldDescriptor, entries protoreflect.Map) {
	type entry struct {
		key   protoreflect.MapKey
		value protoreflect.Value
	}
	var all []entry
	entries.Range(func(key protoreflect.MapKey, value protoreflect.Value) bool {
		all = append(all, entry{key: key, value: value})
		return true
	})
	for _, e := range all {
		key := e.key
		if field.MapKey().Kind() == protoreflect.StringKind {
			entries.Clear(key)
			key = protoreflect.ValueOfString(strings.ToValidUTF8(key.String(), "�")).MapKey()
		}
		entries.Set(key, validUTF8Value(field.MapValue(), e.value))
	}
}

func validUTF8Value(field protoreflect.FieldDescriptor, value protoreflect.Value) protoreflect.Value {
	switch field.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(strings.ToValidUTF8(value.String(), "�"))
	case protoreflect.MessageKind, protoreflect.GroupKind:
		replaceInvalidUTF8(value.Message())
	}
	return value
}

func envelopeJSON(ev *streamv1.RunEvent) (string, error) {
	operation, err := operationJSON(ev.GetOperation())
	if err != nil {
		return "", err
	}
	cli := proto.CloneOf(ev)
	cli.Operation = nil
	rest, err := protojson.Marshal(cli)
	if err != nil {
		return "", err
	}
	return compactObject([]string{`"operation":` + operation}, rest)
}

func operationJSON(op *progressv1.OperationEvent) (string, error) {
	body := proto.CloneOf(op)
	if body == nil {
		body = &progressv1.OperationEvent{}
	}
	body.Time, body.Level, body.Phase, body.Subject, body.Message = nil, 0, 0, "", ""
	fields := []proto.Message{
		op.GetTime(),
		wrapperspb.String(op.GetLevel().String()),
		wrapperspb.String(op.GetPhase().String()),
		wrapperspb.String(op.GetSubject()),
		wrapperspb.String(op.GetMessage()),
	}
	parts := make([]string, 0, 6)
	for i, key := range []string{"time", "level", "phase", "subject", "message"} {
		value, err := protojson.Marshal(fields[i])
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%q:%s", key, value))
	}
	rest, err := protojson.Marshal(body)
	if err != nil {
		return "", err
	}
	return compactObject(parts, rest)
}

func compactObject(parts []string, rest []byte) (string, error) {
	if inner := bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimPrefix(bytes.TrimSpace(rest), []byte("{")), []byte("}"))); len(inner) > 0 {
		parts = append(parts, string(inner))
	}
	var stable bytes.Buffer
	if err := json.Compact(&stable, []byte("{"+strings.Join(parts, ",")+"}")); err != nil {
		return "", err
	}
	return stable.String(), nil
}
