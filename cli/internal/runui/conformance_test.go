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

func TestTheNDJSONProjectionIsOneProtojsonLinePerEnvelopeWrittenAsItLands(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)

			var out bytes.Buffer
			s := NewStream(&out, Presentation{Format: FormatJSON, Width: defaultWidth})
			for i, ev := range events {
				s.Emit(ev)

				written := out.String()
				if !strings.HasSuffix(written, "\n") {
					t.Fatalf("after envelope %d the stream ends mid-line — ndjson must never buffer:\n%s", i, written)
				}
				lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
				if len(lines) != i+1 {
					t.Fatalf("after envelope %d the stream holds %d lines, want one line per envelope emitted so far", i, len(lines))
				}

				want, err := protojson.Marshal(normalize(ev))
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, want); err != nil {
					t.Fatalf("compact: %v", err)
				}
				if got := lines[i]; got != compact.String() {
					t.Fatalf("line %d is not the protojson of its envelope.\n--- got ---\n%s\n--- want ---\n%s", i, got, compact.String())
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
			r.waits = append(r.waits, "waiting "+ev.GetWaiting().GetReason())
		case ev.GetResumed() != nil:
			r.waits = append(r.waits, "resumed "+ev.GetResumed().GetReason())
		case ev.GetResult() != nil:
			res := ev.GetResult()
			r.results = append(r.results, fmt.Sprintf("success=%v interrupted=%v headline=%q detail=%q duration_ms=%d",
				res.GetSuccess(), res.GetInterrupted(), res.GetHeadline(), res.GetDetail(), res.GetDurationMs()))
		case ev.GetOperation().GetStagePlan() != nil:
			for _, st := range ev.GetOperation().GetStagePlan().GetStages() {
				id := hex.EncodeToString(st.GetId())
				if _, seen := titles[id]; seen {
					continue
				}
				titles[id] = phaseTitle(st)
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

type phaseBlock struct {
	id     string
	path   string
	lines  []string
	closed bool
	mark   string
}

func blocksOnTheStream(events []*streamv1.RunEvent) (blocks map[string]*phaseBlock, declared, flushed []string) {
	blocks = map[string]*phaseBlock{}
	units := map[string]string{}
	holder := map[string]string{}
	claim := func(stageID []byte, text string) {
		b := blocks[holder[hex.EncodeToString(stageID)]]
		if b == nil || b.closed {
			return
		}
		b.lines = append(b.lines, strings.Split(strings.TrimSuffix(text, "\n"), "\n")...)
	}
	close := func(id, mark string) {
		b := blocks[id]
		if b == nil || b.closed {
			return
		}
		b.closed, b.mark = true, mark
		flushed = append(flushed, id)
	}

	for _, raw := range events {
		ev := normalize(raw)
		op := ev.GetOperation()
		switch {
		case op.GetStagePlan() != nil:
			for _, st := range op.GetStagePlan().GetStages() {
				id, parent := hex.EncodeToString(st.GetId()), hex.EncodeToString(st.GetParentId())
				switch {
				case parent == "":
					units[id] = st.GetTitle()
				case units[parent] != "":
					blocks[id] = &phaseBlock{id: id, path: units[parent] + " › " + phaseTitle(st)}
					holder[id] = id
					declared = append(declared, id)
				default:
					holder[id] = holder[parent]
				}
			}
		case op.GetProgress() != nil:
			p := op.GetProgress()
			if line := progressLogLine(p.GetMessage(), p.GetCurrent(), p.Total); line != "" {
				claim(p.GetStageId(), line)
			}
		case op.GetLog() != nil:
			claim(op.GetLog().GetStageId(), op.GetLog().GetMessage())
		case op.GetSpan() != nil:
			mark := okMark
			if op.GetSpan().GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR {
				mark = failMark
			}
			close(hex.EncodeToString(op.GetSpan().GetSpanId()), mark)
		case ev.GetResult() != nil:
			mark := warnMark
			if !ev.GetResult().GetSuccess() && !ev.GetResult().GetInterrupted() {
				mark = failMark
			}
			for _, id := range declared {
				close(id, mark)
			}
		}
	}
	return blocks, declared, flushed
}

func bodyBefore(lines []string, at int, n int) []string {
	body := lines[max(at-n, 0):at]
	out := make([]string, 0, len(body))
	for _, line := range body {
		out = append(out, strings.TrimPrefix(line, blockIndent))
	}
	return out
}

func findFrom(lines []string, prefix string, at int) int {
	for i := at; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], prefix) {
			return i
		}
	}
	return -1
}

func closeLine(b *phaseBlock) string {
	switch b.mark {
	case okMark:
		return okMark + " " + b.path + "  "
	case failMark:
		return failMark + " " + b.path + " "
	default:
		return warnMark + " " + b.path + " "
	}
}

func TestCommittedOutputIsWholeBlocksInPhaseCompletionOrder(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, events := fixtureStream(t, name)
			blocks, _, flushed := blocksOnTheStream(events)
			if len(flushed) == 0 {
				t.Fatalf("fixture %s flushes no block — it cannot hold this projection to account", name)
			}

			for _, projection := range []struct{ name, text string }{
				{"plain", projectPlain(t, events)},
				{"live", scrollback(projectLive(t, events))},
			} {
				lines := strings.Split(strings.TrimSuffix(projection.text, "\n"), "\n")
				at := 0
				for _, id := range flushed {
					b := blocks[id]
					want := closeLine(b)
					found := findFrom(lines, want, at)
					if found < 0 {
						t.Fatalf("%s output has no %q at or after line %d, so the blocks do not land in phase-completion order:\n%s",
							projection.name, want, at, projection.text)
					}
					body := bodyBefore(lines, found, len(b.lines))
					if strings.Join(body, "\n") != strings.Join(b.lines, "\n") {
						t.Errorf("%s block %q is not the whole of what the stream gave it, contiguous.\n--- got ---\n%s\n--- want ---\n%s",
							projection.name, b.path, strings.Join(body, "\n"), strings.Join(b.lines, "\n"))
					}
					at = found + 1
				}
			}
		})
	}
}

func TestAnInterruptedRunFlushesEveryInFlightBlockWithAnInterruptedMarker(t *testing.T) {
	t.Parallel()
	for _, name := range fixtureNames(t) {
		_, events := fixtureStream(t, name)
		interrupted := false
		for _, ev := range events {
			interrupted = interrupted || ev.GetResult().GetInterrupted()
		}
		if !interrupted {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			blocks, _, flushed := blocksOnTheStream(events)

			var inFlight []*phaseBlock
			for _, id := range flushed {
				if b := blocks[id]; b.mark == warnMark {
					inFlight = append(inFlight, b)
				}
			}
			if len(inFlight) == 0 {
				t.Fatalf("fixture %s is interrupted with no block in flight — it cannot hold this projection to account", name)
			}

			for _, projection := range []struct{ name, text string }{
				{"plain", projectPlain(t, events)},
				{"live", scrollback(projectLive(t, events))},
			} {
				lines := strings.Split(strings.TrimSuffix(projection.text, "\n"), "\n")
				for _, b := range inFlight {
					marker := warnMark + " " + b.path + " interrupted"
					found := findFrom(lines, marker, 0)
					if found < 0 {
						t.Errorf("%s output does not flush the in-flight block %q with an interrupted marker:\n%s",
							projection.name, b.path, projection.text)
						continue
					}
					body := bodyBefore(lines, found, len(b.lines))
					if strings.Join(body, "\n") != strings.Join(b.lines, "\n") {
						t.Errorf("%s interrupts block %q without flushing what the stream gave it.\n--- got ---\n%s\n--- want ---\n%s",
							projection.name, b.path, strings.Join(body, "\n"), strings.Join(b.lines, "\n"))
					}
				}
			}
		})
	}
}
