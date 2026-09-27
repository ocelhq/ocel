package runui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/ocelhq/ocel/cli/internal/events"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type liveFeed struct{ live *liveLine }

func (f liveFeed) Receive(ev *streamv1.RunEvent) { f.live.observe(ev) }

func (liveFeed) Close() error { return nil }

func liveRun(t *testing.T) (*events.Run, *liveLine, *clock) {
	t.Helper()
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	live := newLiveLine(c.now)
	_, run := onABus(t, context.Background(), c.now, liveFeed{live})
	return run, live, c
}

func say(t *testing.T, unit *events.Scope, lines string) {
	t.Helper()
	w := unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT)
	if _, err := w.Write([]byte(lines)); err != nil {
		t.Fatalf("Write() = %v", err)
	}
}

func shownText(t *testing.T, live *liveLine, width int) string {
	t.Helper()
	line := live.render(width)
	if got := ansi.StringWidth(line); got != width-1 {
		t.Fatalf("render(%d) = %q is %d columns wide, want %d", width, line, got, width-1)
	}
	return strings.TrimRight(line[:strings.LastIndex(line, liveGutter)], " ")
}

func TestTheLiveLineNamesThePhaseItsDoneOverTotalAndTheUnitThatSpokeLast(t *testing.T) {
	t.Parallel()

	run, live, c := liveRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	api := build.Unit("api", "Building api")
	say(t, web, "Compiled successfully\n")
	web.End(nil)
	say(t, api, "\x1b[34m=> [builder 6/6] RUN npm run build\x1b[0m\n")
	c.pass(37600 * time.Millisecond)

	got := live.render(64)
	if want := "⠦ [build] 1/2 · api  => [builder 6/6] RUN npm run build     38s"; got != want {
		t.Fatalf("render(64) =\n%q\nwant\n%q", got, want)
	}
	if width := ansi.StringWidth(got); width != 63 {
		t.Errorf("display width = %d, want 63: the live line never fills the last column", width)
	}
}

func TestASecondUnitSpeakingWithinASecondAndAHalfDoesNotStealTheLine(t *testing.T) {
	t.Parallel()

	run, live, c := liveRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	api := build.Unit("api", "Building api")
	say(t, web, "Creating an optimized production build\n")
	c.pass(time.Second)
	say(t, api, "=> [builder 2/6] COPY package.json .\n")
	c.pass(400 * time.Millisecond)

	if got, want := shownText(t, live, 120), "⠼ [build] 0/2 · web  Creating an optimized production build"; got != want {
		t.Fatalf("1.4s after web spoke the line reads\n%q\nwant\n%q", got, want)
	}

	c.pass(100 * time.Millisecond)
	if got, want := shownText(t, live, 120), "⠴ [build] 0/2 · api  => [builder 2/6] COPY package.json ."; got != want {
		t.Fatalf("1.5s after web spoke the line reads\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitWithoutOutputShowsItsLatestProgressMessage(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	api := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("api", "Deploying api")
	api.Say("Uploading 2 of 4 assets")
	api.Say("Uploading 3 of 4 assets")

	if got, want := shownText(t, live, 60), "⠋ [deploy] 0/1 · api  Uploading 3 of 4 assets"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitsRawOutputOutranksItsProgressMessages(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	say(t, web, "Compiled successfully\n")
	web.Say("Collected 12 routes")

	if got, want := shownText(t, live, 60), "⠋ [build] 0/1 · web  Compiled successfully"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAMultiLineProgressMessageShowsOnlyItsFirstLine(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	api := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("api", "Deploying api")
	api.Say("Waiting for the certificate\nDNS has not propagated yet")

	if got, want := shownText(t, live, 80), "⠋ [deploy] 0/1 · api  Waiting for the certificate"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestDebugOutputNeverReachesTheLiveLine(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	api := run.Phase(progressv1.Phase_PHASE_PROVISION).Unit("api", "Provisioning api")
	api.Say("Applying 12 changes")
	w := api.Output(progressv1.Level_LEVEL_DEBUG, progressv1.Stream_STREAM_STDERR)
	if _, err := w.Write([]byte("I0927 engine: refreshing aws:s3/bucket:Bucket\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	api.Debug("plugin aws 6.66.0 loaded")

	if got, want := shownText(t, live, 80), "⠋ [provision] 0/1 · api  Applying 12 changes"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestWhenTheShownUnitEndsTheLineNamesAUnitStillRunning(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	web := build.Unit("web", "Building web")
	build.Unit("api", "Building api")
	say(t, web, "Compiled successfully\n")
	web.End(nil)

	if got, want := shownText(t, live, 80), "⠋ [build] 1/2 · api"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitThatHasSaidNothingGivesTheLineToOneThatSpeaksAtOnce(t *testing.T) {
	t.Parallel()

	run, live, c := liveRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Unit("web", "Building web")
	if got, want := shownText(t, live, 80), "⠋ [build] 0/1 · web"; got != want {
		t.Fatalf("before anyone spoke the live line shows\n%q\nwant\n%q", got, want)
	}
	api := build.Unit("api", "Building api")
	c.pass(200 * time.Millisecond)
	say(t, api, "=> [builder 1/6] FROM node:22-alpine\n")

	if got, want := shownText(t, live, 80), "⠹ [build] 0/2 · api  => [builder 1/6] FROM node:22-alpine"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAWideLineIsCutToOneColumnShortOfTheTerminalByDisplayWidth(t *testing.T) {
	t.Parallel()

	run, live, c := liveRun(t)
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	say(t, web, "✓ 构建完成 🎉 生成了 12 个页面 👍🏽 用时 3.2 秒 and a long English tail after it\n")
	c.pass(95 * time.Second)

	for width := 1; width <= 120; width++ {
		got := live.render(width)
		columns := ansi.StringWidth(got)
		if columns > max(width-1, 0) {
			t.Errorf("render(%d) = %q is %d columns wide, over %d", width, got, columns, width-1)
		}
		if width >= 24 && (columns != width-1 || !strings.HasSuffix(got, liveGutter+"1m35s")) {
			t.Errorf("render(%d) = %q (%d columns), want %d columns ending in the phase's elapsed time", width, got, columns, width-1)
		}
	}
}

func TestAUnitWithoutASubjectIsNamedByWhatItDoes(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	run.Phase(progressv1.Phase_PHASE_PROVISION).Unit("", "Shared infrastructure")

	if got, want := shownText(t, live, 80), "⠋ [provision] 0/1 · Shared infrastructure"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitOutsideAnyPhaseIsShownWithoutABracket(t *testing.T) {
	t.Parallel()

	c := &clock{at: time.Unix(1_700_000_000, 0)}
	live := newLiveLine(c.now)
	live.observe(&streamv1.RunEvent{
		SpanId:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Subject: "aws",
		Message: "Checking credentials",
		Body:    &streamv1.RunEvent_Started{Started: &progressv1.Started{}},
	})

	if got, want := shownText(t, live, 80), "⠋ 0/1 · aws"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}
