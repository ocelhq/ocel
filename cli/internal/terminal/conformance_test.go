package terminal

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func fixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join("testdata", "streams", "*.ndjson"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no recorded streams under testdata/streams (glob err = %v)", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(filepath.Base(e), ".ndjson"))
	}
	sort.Strings(names)
	return names
}

func fixtureStream(t *testing.T, name string) (raw string, events []*streamv1.RunEvent) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "streams", name+".ndjson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b), parseNDJSON(t, string(b))
}

func golden(t *testing.T, name, ext, got string) {
	t.Helper()
	path := filepath.Join("testdata", "streams", name+ext)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Errorf("%s%s does not match the projection of its stream.\n--- got ---\n%s\n--- want ---\n%s", name, ext, got, want)
	}
}

func projectPlain(t *testing.T, events []*streamv1.RunEvent) string {
	t.Helper()
	var out bytes.Buffer
	s := newTranscript(&out, Presentation{Format: FormatHuman, Width: defaultColumns}, nil)
	for _, ev := range events {
		s.Receive(ev)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return out.String()
}

func TestPlainOutputIsReconstructibleFromTheSerializedStream(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)
			golden(t, name, ".plain", projectPlain(t, events))
		})
	}
}

func TestTheNDJSONProjectionIsOneProtojsonLinePerEnvelopeWrittenAsItLands(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)

			var out safeBuffer
			s := NewJSONLines(&out)
			for i, ev := range events {
				s.Receive(ev)

				written := out.String()
				if !strings.HasSuffix(written, "\n") {
					t.Fatalf("after envelope %d the stream ends mid-line — ndjson must never buffer:\n%s", i, written)
				}
				lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
				if len(lines) != i+1 {
					t.Fatalf("after envelope %d the stream has %d lines, want one line per envelope emitted so far", i, len(lines))
				}

				got := &streamv1.RunEvent{}
				if err := protojson.Unmarshal([]byte(lines[i]), got); err != nil {
					t.Fatalf("line %d %q is not protojson: %v", i, lines[i], err)
				}
				want := proto.CloneOf(ev)
				want.Time = got.GetTime()
				if !proto.Equal(got, want) {
					t.Fatalf("line %d is not the protojson of its envelope.\n--- got ---\n%s\n--- want ---\n%s", i, lines[i], protojson.Format(want))
				}
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
		})
	}
}

type reconstruction struct {
	stages  []string
	plan    []string
	waits   []string
	results []string
}

func reconstruct(events []*streamv1.RunEvent) reconstruction {
	var r reconstruction
	titles := map[string]string{}
	parents := map[string]string{}
	var order []string
	for _, ev := range events {
		switch {
		case ev.GetPlan() != nil:
			for _, g := range ev.GetPlan().GetGroups() {
				for _, c := range g.GetChanges() {
					r.plan = append(r.plan, fmt.Sprintf("%s/%s %s %s", g.GetKind(), g.GetName(), c.GetAction(), c.GetKind()+"/"+c.GetName()))
				}
			}
		case ev.GetWaiting() != nil:
			r.waits = append(r.waits, fmt.Sprintf("waiting on %d unset, remedy %q", len(ev.GetWaiting().GetMissing().GetCells()), ev.GetWaiting().GetMissing().GetRemedy()))
		case ev.GetResumed() != nil:
			r.waits = append(r.waits, "resumed "+ev.GetResumed().GetReason())
		case ev.GetSummary() != nil:
			res := ev.GetSummary()
			r.results = append(r.results, fmt.Sprintf("success=%v interrupted=%v headline=%q detail=%q duration_ms=%d",
				res.GetSuccess(), res.GetInterrupted(), res.GetHeadline(), res.GetDetail(), res.GetDurationMs()))
		case ev.GetStarted() != nil:
			id := hex.EncodeToString(ev.GetSpanId())
			if _, seen := titles[id]; seen {
				continue
			}
			titles[id] = spanTitle(ev)
			parents[id] = hex.EncodeToString(ev.GetStarted().GetParentSpanId())
			order = append(order, id)
		}
	}
	for _, id := range order {
		depth := 0
		for at := parents[id]; at != ""; at = parents[at] {
			depth++
		}
		r.stages = append(r.stages, strings.Repeat("  ", depth)+titles[id])
	}
	return r
}

func TestTheScopeTreePlanWaitsAndResultsComeBackFromNDJSONAlone(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)
			r := reconstruct(events)

			var b strings.Builder
			for _, section := range []struct {
				name  string
				lines []string
			}{
				{"stages", r.stages},
				{"plan", r.plan},
				{"waits", r.waits},
				{"results", r.results},
			} {
				fmt.Fprintf(&b, "%s:\n", section.name)
				for _, line := range section.lines {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			}
			golden(t, name, ".reconstructed", b.String())
		})
	}
}

func spanTitle(ev *streamv1.RunEvent) string {
	if ev.GetMessage() != "" {
		return ev.GetMessage()
	}
	return "[" + phases[ev.GetPhase()].name + "]"
}
