package runtrace

import (
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

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
	return ev
}
