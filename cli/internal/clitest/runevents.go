package clitest

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func RunEvents(t *testing.T, out string) []*streamv1.RunEvent {
	t.Helper()
	var evs []*streamv1.RunEvent
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		evs = append(evs, ev)
	}
	return evs
}
