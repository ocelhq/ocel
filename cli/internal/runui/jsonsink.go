package runui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type JSONSink struct {
	w io.Writer
}

func NewJSONSink(w io.Writer) *JSONSink {
	return &JSONSink{w: w}
}

func (s *JSONSink) Receive(ev *streamv1.RunEvent) {
	line, err := envelopeJSON(normalize(ev))
	if err != nil {
		return
	}
	fmt.Fprintln(s.w, line)
}

func (s *JSONSink) Close() error { return nil }

func envelopeJSON(ev *streamv1.RunEvent) (string, error) {
	body := proto.CloneOf(ev)
	body.Time, body.Level, body.Phase, body.Subject, body.Message = nil, 0, 0, "", ""
	fields := []proto.Message{
		ev.GetTime(),
		wrapperspb.String(ev.GetLevel().String()),
		wrapperspb.String(ev.GetPhase().String()),
		wrapperspb.String(ev.GetSubject()),
		wrapperspb.String(ev.GetMessage()),
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
	if inner := bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimPrefix(bytes.TrimSpace(rest), []byte("{")), []byte("}"))); len(inner) > 0 {
		parts = append(parts, string(inner))
	}
	var stable bytes.Buffer
	if err := json.Compact(&stable, []byte("{"+strings.Join(parts, ",")+"}")); err != nil {
		return "", err
	}
	return stable.String(), nil
}
