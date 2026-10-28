package runtrace

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type messageLines struct{ w io.Writer }

func (m messageLines) Receive(event *streamv1.RunEvent) {
	_, _ = fmt.Fprintln(m.w, event.GetOperation().GetMessage()+event.GetWaiting().GetUrl())
}

func (messageLines) Close() error { return nil }

func said(message string) *streamv1.RunEvent {
	return &streamv1.RunEvent{Operation: &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_INFO, Message: message}}
}

func readLog(t *testing.T, r *Trace) string {
	t.Helper()
	raw, err := os.ReadFile(r.LogPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestARunsLogHoldsEachLineItRendersTheMomentItLandsStampedWithItsTime(t *testing.T) {
	r := startRun(t)
	if err := r.RenderLog(func(w io.Writer) Renderer { return messageLines{w} }); err != nil {
		t.Fatal(err)
	}

	r.Receive(Call{}, said("Building app web"))

	stamped := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z Building app web\n$`)
	if got := readLog(t, r); !stamped.MatchString(got) {
		t.Errorf("the log before the run closed = %q, want the line stamped with its UTC time", got)
	}
	if info, err := os.Stat(r.LogPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("stat %s = %v, %v, want it readable by its owner alone", LogFileName, info, err)
	}
}

func TestARunsLogNeverHoldsAValueTheRunHidesOrAPagesFragment(t *testing.T) {
	r := startRun(t)
	if err := r.RenderLog(func(w io.Writer) Renderer { return messageLines{w} }); err != nil {
		t.Fatal(err)
	}
	const secret = "pw5c-the-database-password"
	AddSecretValues(r.ContextWithRootSpan(context.Background()), secret)

	r.Receive(Call{}, said("connecting with "+secret))
	r.Receive(Call{}, &streamv1.RunEvent{Cli: &streamv1.RunEvent_Waiting{Waiting: &streamv1.WaitingEvent{Url: "http://127.0.0.1:4100/#token=editor-session-token"}}})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	log := readLog(t, r)
	for _, hidden := range []string{secret, "editor-session-token"} {
		if strings.Contains(log, hidden) {
			t.Errorf("the run's log holds %q:\n%s", hidden, log)
		}
	}
	if !strings.Contains(log, "connecting with [secret]") {
		t.Errorf("the run's log =\n%s\nwant the line with its secret hidden", log)
	}
}

func TestARunsLogIsWrittenUnderTraceOffToo(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, string(tracing.ModeOff))
	r := startRun(t)
	if err := r.RenderLog(func(w io.Writer) Renderer { return messageLines{w} }); err != nil {
		t.Fatal(err)
	}
	r.Receive(Call{}, said("Building app web"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, r); !strings.Contains(got, "Building app web") {
		t.Errorf("the run's log = %q, want the line written with the trace off", got)
	}
}
