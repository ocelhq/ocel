package runtrace

import (
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func (t *Trace) Receive(ev *streamv1.RunEvent) {
	t.record(ev)
	switch {
	case ev.GetStarted() != nil:
		t.remember(ev)
	case ev.GetEnded() != nil:
		t.ingestEnded(ev)
	}
}

func (t *Trace) record(ev *streamv1.RunEvent) {
	raw, err := protojson.Marshal(persisted(ev))
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	t.logMu.Lock()
	defer t.logMu.Unlock()
	_, _ = t.logFile.Write(raw)
}

func persisted(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev = proto.CloneOf(ev)
	if waiting := ev.GetWaiting(); waiting != nil {
		waiting.Url, _, _ = strings.Cut(waiting.GetUrl(), "#")
	}
	return ev
}
