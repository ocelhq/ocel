package runui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
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

type terminal struct {
	t      *testing.T
	width  int
	lines  [][]rune
	line   int
	offset int
}

func newTerminal(t *testing.T, width int) *terminal {
	return &terminal{t: t, width: width, lines: [][]rune{nil}}
}

func screenOf(t *testing.T, written string) string {
	t.Helper()
	term := newTerminal(t, math.MaxInt32)
	term.write(written)
	return term.screen()
}

func (term *terminal) row() int { return term.offset / term.width }

func (term *terminal) rowsOf(cells []rune) int {
	return max(1, (len(cells)+term.width-1)/term.width)
}

func (term *terminal) resize(width int) { term.width = width }

func (term *terminal) write(written string) {
	term.t.Helper()
	for rest := written; rest != ""; {
		if strings.HasPrefix(rest, "\x1b[") {
			end := strings.IndexFunc(rest[2:], func(r rune) bool { return r >= '@' && r <= '~' })
			if end < 0 {
				term.t.Fatalf("unterminated escape in %q", rest)
			}
			params, final := rest[2:2+end], rest[2+end]
			rest = rest[3+end:]
			term.escape(params, final)
			continue
		}
		r := []rune(rest)[0]
		rest = rest[len(string(r)):]
		cells := term.lines[term.line]
		switch r {
		case '\r':
			term.offset = term.row() * term.width
		case '\n':
			if next := (term.row() + 1) * term.width; next < len(cells) {
				term.offset = next
				continue
			}
			term.line, term.offset = term.line+1, 0
			if term.line == len(term.lines) {
				term.lines = append(term.lines, nil)
			}
		default:
			for len(cells) <= term.offset {
				cells = append(cells, ' ')
			}
			cells[term.offset] = r
			term.lines[term.line] = cells
			term.offset++
		}
	}
}

func (term *terminal) escape(params string, final byte) {
	term.t.Helper()
	cells := term.lines[term.line]
	switch final {
	case 'K':
		end := (term.row() + 1) * term.width
		if len(cells) <= end {
			term.lines[term.line] = cells[:min(term.offset, len(cells))]
			return
		}
		for i := term.offset; i < end; i++ {
			cells[i] = ' '
		}
	case 'J':
		term.lines[term.line] = cells[:min(term.offset, len(cells))]
		term.lines = term.lines[:term.line+1]
	case 'A':
		n, err := strconv.Atoi(params)
		if err != nil {
			term.t.Fatalf("cursor up %q: %v", params, err)
		}
		column, row := term.offset%term.width, term.row()-n
		for row < 0 && term.line > 0 {
			term.line--
			row += term.rowsOf(term.lines[term.line])
		}
		term.offset = max(row, 0)*term.width + column
	case 'm', 'h', 'l':
	default:
		term.t.Fatalf("unexpected escape %q", final)
	}
}

func (term *terminal) screen() string {
	lines := make([]string, len(term.lines))
	for i, cells := range term.lines {
		lines[i] = string(cells)
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

	want := "\x1b[?2026h\r\x1b[K⠋ [build]                    0s\x1b[?2026l" +
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

	want := "\x1b[?2026h\r\x1b[K\x1b[36m⠋\x1b[0m [build]" + strings.Repeat(" ", 20) + "0s\x1b[?2026l" +
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

func TestTheRunsResultErasesTheLiveLineAndNoTickDrawsItAgain(t *testing.T) {
	t.Parallel()

	var transcript bytes.Buffer
	present := Presentation{Width: 32}
	rig := newLineRig(t, present, newGroupedSink(&transcript, present, nil))
	build := rig.run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Unit("web", "Building web").End(nil)
	build.End(nil)
	var err error
	rig.run.End(&err)
	ended := rig.out.String()
	rig.clock.pass(100 * time.Millisecond)
	rig.tick()

	if got, want := screenOf(t, ended), transcript.String(); got != want {
		t.Fatalf("screen once the run ended\n%q\nwant the grouped transcript alone\n%q", got, want)
	}
	if got := rig.closed(t); got != ended {
		t.Fatalf("after the result the sink wrote %q, want nothing more", strings.TrimPrefix(got, ended))
	}
}

func TestAResizeClearsEveryRowTheLiveLineAndItsCursorWrappedInto(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ from, to int }{{80, 40}, {81, 40}, {81, 27}, {121, 40}, {60, 20}, {41, 40}} {
		t.Run(fmt.Sprintf("%d to %d columns", tc.from, tc.to), func(t *testing.T) {
			t.Parallel()

			var transcript bytes.Buffer
			present := Presentation{Width: tc.from}
			grouped := newGroupedSink(&transcript, present, nil)
			rig := newLineRig(t, present, grouped)
			build := rig.run.Phase(progressv1.Phase_PHASE_BUILD)
			build.Say("Resolved 3 apps")
			build.Unit("web", "Building web")
			before := rig.out.String()
			rig.resize(tc.to)
			written := rig.closed(t)

			term := newTerminal(t, tc.from)
			term.write(before)
			term.resize(tc.to)
			term.write(strings.TrimPrefix(written, before))
			if err := grouped.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			if got, want := term.screen(), transcript.String(); got != want {
				t.Fatalf("screen after the resize and close\n%q\nwant the transcript alone\n%q", got, want)
			}
		})
	}
}
