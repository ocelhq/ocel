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
	live := newLiveLine(c.now, Presentation{})
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

func TestTheLiveLineNamesThePhaseTheUnitThatSpokeLastAndItsDoneOverTotal(t *testing.T) {
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
	if want := "      [build] ⠦ api: => [builder 6/6] RUN npm run bu  1/2 · 38s"; got != want {
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

	if got, want := shownText(t, live, 120), "      [build] ⠼ web: Creating an optimized production build"; got != want {
		t.Fatalf("1.4s after web spoke the line reads\n%q\nwant\n%q", got, want)
	}

	c.pass(100 * time.Millisecond)
	if got, want := shownText(t, live, 120), "      [build] ⠴ api: => [builder 2/6] COPY package.json ."; got != want {
		t.Fatalf("1.5s after web spoke the line reads\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitWithoutOutputShowsItsLatestProgressMessage(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	api := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("api", "Deploying api")
	api.Say("Uploading 2 of 4 assets")
	api.Say("Uploading 3 of 4 assets")

	if got, want := shownText(t, live, 60), "      [deploy] ⠋ api: Uploading 3 of 4 assets"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitsRawOutputOutranksItsProgressMessages(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")
	say(t, web, "Compiled successfully\n")
	web.Say("Collected 12 routes")

	if got, want := shownText(t, live, 60), "      [build] ⠋ web: Compiled successfully"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAMultiLineProgressMessageShowsOnlyItsFirstLine(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	api := run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("api", "Deploying api")
	api.Say("Waiting for the certificate\nDNS has not propagated yet")

	if got, want := shownText(t, live, 80), "      [deploy] ⠋ api: Waiting for the certificate"; got != want {
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

	if got, want := shownText(t, live, 80), "      [provision] ⠋ api: Applying 12 changes"; got != want {
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

	if got, want := shownText(t, live, 80), "      [build] ⠋ api: Building api"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitThatHasSaidNothingGivesTheLineToOneThatSpeaksAtOnce(t *testing.T) {
	t.Parallel()

	run, live, c := liveRun(t)
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	build.Unit("web", "Building web")
	if got, want := shownText(t, live, 80), "      [build] ⠋ web: Building web"; got != want {
		t.Fatalf("before anyone spoke the live line shows\n%q\nwant\n%q", got, want)
	}
	api := build.Unit("api", "Building api")
	c.pass(200 * time.Millisecond)
	say(t, api, "=> [builder 1/6] FROM node:22-alpine\n")

	if got, want := shownText(t, live, 80), "      [build] ⠹ api: => [builder 1/6] FROM node:22-alpine"; got != want {
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
		columns := max(ansi.StringWidth(got), ansi.StringWidthWc(got))
		if columns > max(width-1, 0) {
			t.Errorf("render(%d) = %q is %d columns wide, over %d", width, got, columns, width-1)
		}
		if width >= 33 && (columns != width-1 || !strings.HasSuffix(got, liveGutter+"0/1 · 1m35s")) {
			t.Errorf("render(%d) = %q (%d columns), want %d columns ending in the phase's tally and elapsed time", width, got, columns, width-1)
		}
	}
}

func TestAnEmojiLineFitsATerminalThatCountsEachCodePointsWidth(t *testing.T) {
	t.Parallel()

	run, live, c := liveRun(t)
	family := "\U0001f468\u200d\U0001f469\u200d\U0001f467"
	web := run.Phase(progressv1.Phase_PHASE_BUILD).Unit(family+" web", "Building web")
	say(t, web, "👍🏽 built 🧑🏽‍💻 by "+family+" in 3.2s, and a long English tail after it\n")
	c.pass(95 * time.Second)

	for width := 1; width <= 120; width++ {
		got := live.render(width)
		if columns := max(ansi.StringWidth(got), ansi.StringWidthWc(got)); columns > max(width-1, 0) {
			t.Errorf("render(%d) = %q is %d columns wide on some terminal, over %d", width, got, columns, width-1)
		}
		if width >= 33 && !strings.HasSuffix(got, liveGutter+"0/1 · 1m35s") {
			t.Errorf("render(%d) = %q, want it to end in the phase's elapsed time", width, got)
		}
	}
}

func TestAtAnyWidthTheLiveLineKeepsItsSpinnerOrShowsNothing(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	run.Phase(progressv1.Phase_PHASE_BUILD).Unit("web", "Building web")

	for width := 0; width <= 24; width++ {
		if got := live.render(width); got != "" && !strings.HasPrefix(got, "      [build] "+spinnerFrame(0)) {
			t.Errorf("render(%d) = %q, want it to keep the spinner or be empty", width, got)
		}
	}
}

func TestTheLiveLinesPhaseLinesUpWithTheLinesCommittedAboveIt(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("api", "Deploying api")
	committed := line{level: progressv1.Level_LEVEL_INFO, phase: progressv1.Phase_PHASE_DEPLOY, subject: "web", message: "m", ends: progressv1.SpanStatus_SPAN_STATUS_OK}.render(Presentation{})

	got := live.render(80)
	if strings.Index(got, "[deploy]") != strings.Index(committed, "[deploy]") {
		t.Errorf("live %q and committed %q start their phase in different columns", got, committed)
	}
	if strings.Index(got, spinnerFrame(0)) != strings.Index(committed, okMark) {
		t.Errorf("live %q puts its spinner where committed %q has no mark", got, committed)
	}
}

func TestAUnitThatHasSaidNothingYetShowsWhatItDoesBesideItsSubject(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	run.Phase(progressv1.Phase_PHASE_DEPLOY).Unit("web", "Deploying the serverless app to production")

	if got, want := shownText(t, live, 80), "      [deploy] ⠋ web: Deploying the serverless app to production"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitWithoutASubjectIsNamedByWhatItDoes(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	run.Phase(progressv1.Phase_PHASE_PROVISION).Unit("", "Shared infrastructure")

	if got, want := shownText(t, live, 80), "      [provision] ⠋ Shared infrastructure"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAUnitOutsideAnyPhaseIsShownWithoutABracket(t *testing.T) {
	t.Parallel()

	c := &clock{at: time.Unix(1_700_000_000, 0)}
	live := newLiveLine(c.now, Presentation{})
	live.observe(&streamv1.RunEvent{
		SpanId:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Subject: "aws",
		Message: "Checking credentials",
		Body:    &streamv1.RunEvent_Started{Started: &progressv1.Started{}},
	})

	if got, want := shownText(t, live, 80), "      ⠋ aws: Checking credentials"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}

func TestAPhaseWithNoUnitsYetShowsNoDoneOverTotal(t *testing.T) {
	t.Parallel()

	run, live, _ := liveRun(t)
	run.Phase(progressv1.Phase_PHASE_BUILD)

	if got, want := shownText(t, live, 80), "      [build] ⠋"; got != want {
		t.Fatalf("the live line shows\n%q\nwant\n%q", got, want)
	}
}
