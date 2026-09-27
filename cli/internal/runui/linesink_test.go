package runui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type lineRig struct {
	run     *events.Run
	sink    *LineSink
	out     *bytes.Buffer
	clock   *clock
	ticks   chan time.Time
	resized chan os.Signal
	width   *atomic.Int64
}

func newLineRig(t *testing.T, present Presentation, also ...events.Sink) *lineRig {
	t.Helper()
	r := &lineRig{
		out:     &bytes.Buffer{},
		clock:   &clock{at: time.Unix(1_700_000_000, 0)},
		ticks:   make(chan time.Time),
		resized: make(chan os.Signal),
		width:   &atomic.Int64{},
	}
	r.width.Store(int64(present.Width))
	r.sink = newLineSink(r.out, present, lineSources{
		now:     r.clock.now,
		ticks:   r.ticks,
		resized: r.resized,
		width:   func() int { return int(r.width.Load()) },
	})
	bus := events.NewBus(r.clock.now)
	bus.Attach(r.sink)
	for _, sink := range also {
		bus.Attach(sink)
	}
	_, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	r.run = run
	return r
}

func (r *lineRig) tick() {
	r.ticks <- r.clock.now()
}

func (r *lineRig) resize(width int) {
	r.width.Store(int64(width))
	r.resized <- os.Interrupt
}

func (r *lineRig) closed(t *testing.T) string {
	t.Helper()
	if err := r.sink.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	return r.out.String()
}

func screenOf(t *testing.T, written string) string {
	t.Helper()
	rows := [][]rune{nil}
	row, col := 0, 0
	for rest := written; rest != ""; {
		if strings.HasPrefix(rest, "\x1b[") {
			end := strings.IndexFunc(rest[2:], func(r rune) bool { return r >= '@' && r <= '~' })
			if end < 0 {
				t.Fatalf("unterminated escape in %q", rest)
			}
			params, final := rest[2:2+end], rest[2+end]
			rest = rest[3+end:]
			switch final {
			case 'K':
				rows[row] = rows[row][:min(col, len(rows[row]))]
			case 'J':
				rows[row] = rows[row][:min(col, len(rows[row]))]
				rows = rows[:row+1]
			case 'A':
				n, err := strconv.Atoi(params)
				if err != nil {
					t.Fatalf("cursor up %q: %v", params, err)
				}
				row = max(row-n, 0)
			case 'm', 'h', 'l':
			default:
				t.Fatalf("unexpected escape %q", final)
			}
			continue
		}
		r := []rune(rest)[0]
		rest = rest[len(string(r)):]
		switch r {
		case '\r':
			col = 0
		case '\n':
			row, col = row+1, 0
			if row == len(rows) {
				rows = append(rows, nil)
			}
		default:
			for len(rows[row]) <= col {
				rows[row] = append(rows[row], ' ')
			}
			rows[row][col] = r
			col++
		}
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = string(r)
	}
	return strings.Join(lines, "\n")
}

func TestTheScreenAfterALineRunHoldsExactlyTheGroupedTranscript(t *testing.T) {
	t.Parallel()

	var transcript bytes.Buffer
	present := Presentation{Width: 80}
	grouped := newGroupedSink(&transcript, present, nil)
	rig := newLineRig(t, present, grouped)
	build := rig.run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	api := build.Unit("api", "Building api")
	say(t, web, "> next build\nCompiled successfully\n")
	rig.clock.pass(3 * time.Second)
	say(t, api, "=> [builder 1/6] FROM node:22-alpine\n")
	web.End(nil)
	build.Say("web is built")
	rig.clock.pass(2 * time.Second)
	api.End(errors.New("npm run build exited 1"))
	build.End(nil)
	err := errors.New("api did not build")
	rig.run.End(&err)

	if err := grouped.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got, want := screenOf(t, rig.closed(t)), transcript.String(); got != want {
		t.Fatalf("screen after close\n%s\nwant the grouped transcript\n%s", got, want)
	}
}

func TestACommitErasesTheLivePrintsTheBlockAndRedrawsTheLiveLineInOneSynchronizedFrame(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 32})
	build := rig.run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	rig.clock.pass(2 * time.Second)
	web.End(nil)

	want := "\x1b[?2026h\r\x1b[K⠋ [build] 0/0                0s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[K⠋ [build] 0/1 · web          0s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[KINFO  [build] ✓ web: Building web in 2s\n\r\x1b[K⠋ [build] 1/1                2s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[K\x1b[?2026l"
	if got := rig.closed(t); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestATickRedrawsTheLiveLineOnlyWhenItsFrameChanged(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 32})
	rig.run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	before := rig.out.String()
	rig.clock.pass(100 * time.Millisecond)
	rig.tick()
	rig.tick()

	want := before +
		"\x1b[?2026h\r\x1b[K⠙ [build] 0/1 · web         <1s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[KWARN  [build] web: Building web did not finish\n\x1b[?2026l"
	if got := rig.closed(t); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestAHoldLeavesNoLiveLineOnScreenAndNoTickDrawsOneWhileItLasts(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 32})
	web := rig.run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	before := rig.out.String()
	rig.run.Hold(&streamv1.WaitingEvent{})
	web.Say("Collected 12 routes")
	rig.clock.pass(100 * time.Millisecond)
	rig.tick()

	want := before +
		"\x1b[?2026h\r\x1b[K\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[KWARN  [build] web: Building web did not finish\n      Collected 12 routes\n\x1b[?2026l"
	if got := rig.closed(t); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestTheLiveLineComesBackWhenTheRunResumes(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 32})
	rig.run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	resume := rig.run.Hold(&streamv1.WaitingEvent{})
	before := rig.out.String()
	rig.clock.pass(100 * time.Millisecond)
	resume("consent given")

	want := before + "\x1b[?2026h\r\x1b[K⠙ [build] 0/1 · web         <1s\x1b[?2026l"
	if got := rig.out.String(); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestAResizeToHalfTheWidthClearsBothRowsTheLiveLineWrappedInto(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 80})
	rig.run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	before := rig.out.String()
	if !strings.HasSuffix(before, "⠋ [build] 0/1 · web"+strings.Repeat(" ", 58)+"0s\x1b[?2026l") {
		t.Fatalf("before the resize the live line is not 79 columns: %q", before)
	}
	rig.resize(40)

	want := before +
		"\x1b[?2026h\x1b[1A\r\x1b[J⠋ [build] 0/1 · web" + strings.Repeat(" ", 18) + "0s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[KWARN  [build] web: Building web did not finish\n\x1b[?2026l"
	if got := rig.closed(t); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestAResizeWiderThanTheLiveLineOnlyRedrawsItAtTheNewWidth(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 32})
	rig.run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	before := rig.out.String()
	rig.resize(60)

	want := before +
		"\x1b[?2026h\r\x1b[K⠋ [build] 0/1 · web" + strings.Repeat(" ", 38) + "0s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[KWARN  [build] web: Building web did not finish\n\x1b[?2026l"
	if got := rig.closed(t); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestWithColourTheLiveLinesSpinnerIsCyan(t *testing.T) {
	t.Parallel()

	rig := newLineRig(t, Presentation{Width: 32, Color: true})
	rig.run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")

	want := "\x1b[?2026h\r\x1b[K\x1b[36m⠋\x1b[0m [build] 0/0" + strings.Repeat(" ", 16) + "0s\x1b[?2026l" +
		"\x1b[?2026h\r\x1b[K\x1b[36m⠋\x1b[0m [build] 0/1 · web" + strings.Repeat(" ", 10) + "0s\x1b[?2026l"
	if got := rig.out.String(); got != want {
		t.Fatalf("wrote\n%q\nwant\n%q", got, want)
	}
}

func TestALineSinkOnTheRealClockLeavesOnlyTheTranscriptOnceClosed(t *testing.T) {
	t.Parallel()

	out := &safeBuffer{}
	var transcript bytes.Buffer
	sink := NewLineSink(out, Presentation{Width: 60})
	grouped := newGroupedSink(&transcript, Presentation{Width: 60}, nil)
	bus, run := onABus(t, context.Background(), time.Now, sink)
	bus.Attach(grouped)
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	say(t, web, "Compiled successfully\n")
	web.End(nil)
	if err := bus.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if got, want := screenOf(t, out.String()), transcript.String(); got != want {
		t.Fatalf("screen after close\n%q\nwant the grouped transcript\n%q", got, want)
	}
}
