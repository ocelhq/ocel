package terminal

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
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type JSONLines struct {
	w io.Writer
}

func NewJSONLines(w io.Writer) *JSONLines {
	return &JSONLines{w: w}
}

func (s *JSONLines) Receive(ev *streamv1.RunEvent) {
	line, err := envelopeJSON(ev)
	if err != nil {
		return
	}
	fmt.Fprintln(s.w, line)
}

func (s *JSONLines) Close() error { return nil }

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
