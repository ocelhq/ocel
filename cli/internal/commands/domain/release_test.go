package domain

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestDomainReleaseTearsDownTheGlobalDomainOnlyOnceNothingIsServedOnIt(t *testing.T) {
	t.Run("release refuses while projects still have previews on the wildcard", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")
		t.Setenv(clitest.FakeServedPreviewsEnvVar, "shop, blog")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainRelease(context.Background(), invocation, root, domainOptions{preview: true, yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDomainRelease err = nil, want the release refused; stdout=%s", stdout.String())
		}
		for _, want := range []string{"shop", "blog", "ocel preview rm", "ocel destroy preview"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want the refusal to contain %q", stdout.String(), want)
			}
		}
		if strings.Contains(stdout.String(), "RELEASE DOMAIN") {
			t.Errorf("stdout = %q, want nothing released while previews are still served", stdout.String())
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("release plans, then releases with --yes once nothing is served", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRelease(context.Background(), invocation, root, domainOptions{preview: true, yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRelease err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"This will release *.preview.acme.com",
			"fronted by the relay edge",
			"– preview entry router *.preview.acme.com",
			"This cannot be undone.",
			"1 to delete, 1 unchanged.",
			"RELEASE DOMAIN tier=TIER_PREVIEW",
			"Released *.preview.acme.com",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "you created it yourself") {
			t.Errorf("stdout spent a row on a record nothing touches:\n%s", out)
		}
		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("release refuses non-interactively without --yes", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainRelease(context.Background(), invocation, root, domainOptions{preview: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDomainRelease err = nil, want it to refuse without a terminal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want it to point at --yes", err)
		}
	})
}

func TestReleasingThePreviewDomainAsksForItsNameWhileTheRunIsHeldAfterThePlanItShows(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	invocation := newTestInvocation()
	invocation.StdinIsTerminal = func(io.Reader) bool { return true }
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}
	t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
	t.Setenv(clitest.FakeGlobalDomainEnvVar, "preview.acme.com")

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stream)
	if err := runDomainRelease(context.Background(), invocation, root, domainOptions{preview: true}, &stdout, &stderr, strings.NewReader("preview.acme.com\n")); err != nil {
		t.Fatalf("runDomainRelease err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	evs := clitest.RunEvents(t, stream.String())
	shown := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetPlan() != nil })
	if shown < 0 || evs[shown].GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("the release plan was not shown in the plan phase: %s", stream.String())
	}
	waiting := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil })
	if waiting < shown {
		t.Fatalf("held at event %d, plan shown at %d: want the run held to ask once the plan is shown: %s", waiting, shown, stream.String())
	}
	resumed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if resumed < waiting || evs[resumed].GetResumed().GetReason() != "answered" {
		t.Fatalf("resumed at event %d, held at %d: want the run resumed once answered: %s", resumed, waiting, stream.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() || result.GetHeadline() != "Released *.preview.acme.com" {
		t.Errorf("result = %v, want the run to end reporting the released domain", result)
	}
}
