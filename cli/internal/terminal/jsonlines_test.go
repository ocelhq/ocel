package terminal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/run"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func parseNDJSON(t *testing.T, raw string) []*streamv1.RunEvent {
	t.Helper()
	var out []*streamv1.RunEvent
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestEveryNDJSONLineCarriesTimeLevelPhaseSubjectAndMessageEvenWhenEmpty(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	bus := run.NewBus(time.Now)
	bus.Attach(NewJSONLines(&out))
	_, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	run.Hold(&streamv1.WaitingEvent{})("the page was answered")
	if err := bus.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("line %q is not JSON: %v", lines[len(lines)-1], err)
	}
	envelope, _ := rec["operation"].(map[string]any)
	for key, want := range map[string]any{
		"level":   "LEVEL_INFO",
		"phase":   "PHASE_UNSPECIFIED",
		"subject": "",
		"message": "",
	} {
		if got, ok := envelope[key]; !ok || got != want {
			t.Errorf("%q = %v (present %v), want %q on a line %q", key, got, ok, want, out.String())
		}
	}
	if stamp, _ := envelope["time"].(string); stamp == "" {
		t.Errorf("time = %v, want the moment the event landed on a line %q", envelope["time"], out.String())
	}
	if reason := rec["resumed"].(map[string]any)["reason"]; reason != "the page was answered" {
		t.Errorf("resumed.reason = %v, want the body beside the envelope", reason)
	}
}

func TestADebugLineReachesNDJSONAtItsLevel(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	NewJSONLines(&out).Receive(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_DEBUG, Message: "+  fake:bucket assets creating (0s)"}})
	got := parseNDJSON(t, out.String())
	if len(got) != 1 || got[0].GetOperation().GetLevel() != progressv1.Level_LEVEL_DEBUG || got[0].GetOperation().GetMessage() != "+  fake:bucket assets creating (0s)" {
		var lines []string
		for _, ev := range got {
			lines = append(lines, ev.GetOperation().GetLevel().String()+" "+ev.GetOperation().GetMessage())
		}
		t.Errorf("ndjson = %q, want the one debug line at DEBUG", lines)
	}
}

func TestTheJSONSinkWritesEachEventAsOneLineTheMomentItLands(t *testing.T) {
	t.Parallel()

	var out safeBuffer
	var sink run.Sink = NewJSONLines(&out)
	t.Cleanup(func() { _ = sink.Close() })

	span := []byte{1, 0, 0, 0, 0, 0, 0, 0}
	sink.Receive(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{SpanId: span, Message: "web", Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{}}}})
	if got := out.String(); strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("after one event the sink wrote %q, want exactly one whole line", got)
	}

	sink.Receive(&streamv1.RunEvent{Operation: &progressv1.OperationEvent{SpanId: span, Message: "uploading"}})
	lines := parseNDJSON(t, out.String())
	if len(lines) != 2 || lines[1].GetOperation().GetMessage() != "uploading" {
		t.Errorf("after two events the sink wrote %q, want the second as its own line", out.String())
	}
}
