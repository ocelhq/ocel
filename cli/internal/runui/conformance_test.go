package runui

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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
	s := NewStream(&out, Presentation{Format: FormatHuman, Width: defaultWidth})
	for _, ev := range events {
		s.Emit(ev)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return out.String()
}

func projectLive(t *testing.T, events []*streamv1.RunEvent) string {
	t.Helper()
	var out bytes.Buffer
	s := NewStream(&out, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth})
	for _, ev := range events {
		s.Emit(ev)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return out.String()
}

var eraseSequence = regexp.MustCompile(`^\x1b\[(\d+)A\x1b\[J`)

func scrollback(raw string) string {
	var lines []string
	var cur strings.Builder
	for i := 0; i < len(raw); {
		if m := eraseSequence.FindStringSubmatch(raw[i:]); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n > len(lines) {
				n = len(lines)
			}
			lines = lines[:len(lines)-n]
			i += len(m[0])
			continue
		}
		if raw[i] == '\n' {
			lines = append(lines, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(raw[i])
		i++
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
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

func TestPlainAndLiveCommitTheSameContent(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)
			plain := projectPlain(t, events)
			live := projectLive(t, events)

			if live == plain {
				t.Errorf("the live projection wrote no live window at all — it should differ from plain before the window is stripped")
			}
			if got := scrollback(live); got != plain {
				t.Errorf("what the live run leaves in the scrollback differs from plain output.\n--- live ---\n%s\n--- plain ---\n%s", got, plain)
			}
		})
	}
}

func TestTheNDJSONProjectionIsTheStreamItself(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, events := fixtureStream(t, name)

			var out bytes.Buffer
			s := NewStream(&out, Presentation{Format: FormatJSON, Width: defaultWidth})
			for _, ev := range events {
				s.Emit(ev)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			if got, want := canonicalNDJSON(t, out.String()), canonicalNDJSON(t, raw); got != want {
				t.Errorf("the ndjson projection is not the recorded stream.\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func canonicalNDJSON(t *testing.T, raw string) string {
	t.Helper()
	var lines []string
	for _, ev := range parseNDJSON(t, raw) {
		b, err := protojson.MarshalOptions{}.Marshal(normalize(ev))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var stable bytes.Buffer
		if err := json.Compact(&stable, b); err != nil {
			t.Fatalf("compact: %v", err)
		}
		lines = append(lines, stable.String())
	}
	return strings.Join(lines, "\n") + "\n"
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
			r.waits = append(r.waits, "waiting "+ev.GetWaiting().GetReason())
		case ev.GetResumed() != nil:
			r.waits = append(r.waits, "resumed "+ev.GetResumed().GetReason())
		case ev.GetResult() != nil:
			res := ev.GetResult()
			r.results = append(r.results, fmt.Sprintf("success=%v headline=%q error=%q duration_ms=%d",
				res.GetSuccess(), res.GetHeadline(), res.GetError(), res.GetDurationMs()))
		case ev.GetOperation().GetStagePlan() != nil:
			for _, st := range ev.GetOperation().GetStagePlan().GetStages() {
				id := hex.EncodeToString(st.GetId())
				if _, seen := titles[id]; seen {
					continue
				}
				titles[id] = st.GetTitle()
				parents[id] = hex.EncodeToString(st.GetParentId())
				order = append(order, id)
			}
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

func TestTheStageTreePlanWaitsAndResultsComeBackFromNDJSONAlone(t *testing.T) {
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

func TestEveryPhaseOnTheStreamCommitsAStartLine(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)
			plain := projectPlain(t, events)
			live := scrollback(projectLive(t, events))

			units := map[string]string{}
			var wantStarts []string
			for _, ev := range events {
				sp := ev.GetOperation().GetStagePlan()
				if sp == nil {
					continue
				}
				for _, st := range sp.GetStages() {
					id, parent := hex.EncodeToString(st.GetId()), hex.EncodeToString(st.GetParentId())
					if parent == "" {
						units[id] = st.GetTitle()
						continue
					}
					if unit, ok := units[parent]; ok {
						wantStarts = append(wantStarts, startMark+" "+unit+" › "+phaseTitle(st))
					}
				}
			}
			if len(wantStarts) == 0 {
				t.Fatalf("fixture %s declares no phase — it cannot hold this projection to account", name)
			}
			for _, want := range wantStarts {
				for _, projection := range []struct{ name, text string }{{"plain", plain}, {"live", live}} {
					if !strings.Contains(projection.text, want+"\n") {
						t.Errorf("%s output has no phase-start line %q:\n%s", projection.name, want, projection.text)
					}
				}
			}
		})
	}
}

func phaseTitle(st *progressv1.Stage) string {
	if st.GetTitle() != "" {
		return st.GetTitle()
	}
	return phaseLabel(st.GetPhase())
}
