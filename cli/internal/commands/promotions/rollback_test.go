package promotions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestRollbackMovesProductionToTheChosenPromotionOnceConsented(t *testing.T) {
	t.Run("with no argument it rolls back to the immediately previous promotion", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`This will roll production of project "test-app" back to an earlier deployment`,
			"– live    promo-2",
			"– target  promo-1",
			"tag v1.0.0",
			"web=build-1",
			"Rolled back to promotion promo-1",
			"as promotion " + activePromotionID(t, project),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		active, _, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).ReadActive(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if active.PromotionID == "promo-2" || active.Builds["web"] != "build-1~fp1" {
			t.Errorf("production serves %+v after the rollback, want a new promotion of promo-1's builds", active)
		}
	})

	t.Run("--yes rolls back without asking", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()
		invocation.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if strings.Contains(out, "Roll production of") {
			t.Errorf("stdout = %q, want --yes to skip the confirmation", out)
		}
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want --yes to roll back all the same", out)
		}
	})

	t.Run("--to rolls back to the named promotion once consented to", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()
		invocation.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{to: "promo-1"}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `Roll production of "test-app" back to promotion promo-1?`) {
			t.Errorf("stdout = %q, want the rollback to ask before it moves production", out)
		}
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-1", out)
		}
	})

	t.Run("a declined confirmation rolls nothing back", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()
		invocation.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Not confirmed, so this run changes nothing") {
			t.Errorf("stdout = %q, want a declined confirmation to say so", out)
		}
		if strings.Contains(out, "Rolled back") {
			t.Errorf("stdout = %q, want no rollback behind a declined confirmation", out)
		}
	})

	t.Run("--dry reads the history, prints the plan and rolls nothing back", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"– live    promo-2", "– target  promo-1", "Run without --dry to roll back."} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Rolled back") {
			t.Errorf("stdout = %q, want --dry to roll nothing back", out)
		}
	})

	t.Run("--tag rolls back to the tagged promotion and echoes the tag", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{tag: "v1.0.0", yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Rolled back to promotion promo-1") {
			t.Errorf("stdout = %q, want it to report rolling back to promo-1", out)
		}
		if !strings.Contains(out, "tag v1.0.0") {
			t.Errorf("stdout = %q, want it to echo the target's tag", out)
		}
	})

	t.Run("--to and --tag are mutually exclusive", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{to: "promo-1", tag: "v1.0.0"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want an error when both --to and --tag are set")
		}
		if !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("err = %v, want it to report mutual exclusivity", err)
		}
	})

	t.Run("a tag no promotion has is refused before anything is rolled back", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{tag: "v9.9.9", yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want an error for a tag nothing has")
		}
		for _, want := range []string{`"v9.9.9"`, "v1.0.0"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to name %q — what was asked for and what is there", stdout.String(), want)
			}
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want nothing rolled back", stdout.String())
		}
	})

	t.Run("an unlisted --to is refused before the rollback is asked for", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{to: "no-such-promotion", yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want an error for an unknown promotion id")
		}
		for _, want := range []string{"no-such-promotion", "promo-2, promo-1"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to name %q — what was asked for and what is listed", stdout.String(), want)
			}
		}
		out := stdout.String()
		if strings.Contains(out, "This will roll production") || strings.Contains(out, "Rolled back") {
			t.Errorf("stdout = %q, want the refusal to come before the plan and the rollback", out)
		}
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		project := promotedTwice(t)
		bootstrappedOnlyForPreview(t, project)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want a tier-mismatch error")
		}
		if !strings.Contains(stdout.String(), "this command needs production infrastructure") {
			t.Errorf("stdout = %q, want the concrete tier-mismatch message", stdout.String())
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want no rollback to have been driven against preview infra", stdout.String())
		}
	})

	t.Run("it refuses when the infrastructure is absent", func(t *testing.T) {
		project := promotedTwice(t)
		if err := project.Provider.FakeBootstrap().Remove(context.Background(), environment.TierProduction, nil); err != nil {
			t.Fatal(err)
		}
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback err = nil, want a missing-infrastructure error")
		}
		if !strings.Contains(stdout.String(), "ocel bootstrap production") {
			t.Errorf("stdout = %q, want it to direct the user to `ocel bootstrap production`", stdout.String())
		}
		if strings.Contains(stdout.String(), "Rolled back") {
			t.Errorf("stdout = %q, want no rollback to have been driven", stdout.String())
		}
	})

	t.Run("without --yes it refuses without a terminal", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runRollback without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want the no-TTY refusal to point at --yes", err)
		}
	})
}

func TestARollbackAsksWhileTheRunIsHeldAfterThePlanItShows(t *testing.T) {
	project := promotedTwice(t)
	invocation := clitest.NewInvocation()
	invocation.StdinIsTerminal = func(io.Reader) bool { return true }
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stream)
	if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("runRollback err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	evs := clitest.RunEvents(t, stream.String())
	shown := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool {
		return strings.Contains(ev.GetOperation().GetMessage(), "– target  promo-1")
	})
	if shown < 0 || evs[shown].GetOperation().GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("the rollback plan was not shown in the plan phase: %s", stream.String())
	}
	waiting := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil })
	if waiting < shown {
		t.Fatalf("held at event %d, plan shown at %d: want the run held to ask once the plan is shown: %s", waiting, shown, stream.String())
	}
	resumed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if resumed < waiting || evs[resumed].GetResumed().GetReason() != "answered" {
		t.Fatalf("resumed at event %d, held at %d: want the run resumed once answered: %s", resumed, waiting, stream.String())
	}
	result := evs[len(evs)-1].GetSummary()
	if !result.GetSuccess() || !strings.HasPrefix(result.GetHeadline(), "Rolled back to promotion promo-1") {
		t.Errorf("result = %v, want the run to end reporting the rollback to promo-1", result)
	}
}

func TestARollbackGoesToThePromotionBeforeTheLiveOneAndRefusesWhenThereIsNoneToName(t *testing.T) {
	entry := func(id, tag string, active bool) *contractv1.PromotionHistoryEntry {
		return &contractv1.PromotionHistoryEntry{
			Promotion: &contractv1.Promotion{PromotionId: id, Tag: tag},
			Active:    active,
		}
	}

	t.Run("a tag on more than one promotion names none of them", func(t *testing.T) {
		history := []*contractv1.PromotionHistoryEntry{
			entry("p3", "release", true),
			entry("p2", "release", false),
			entry("p1", "", false),
		}
		_, err := rollbackTarget(history, "", "release")
		if err == nil {
			t.Fatal("rollbackTarget err = nil, want an ambiguous tag refused")
		}
		for _, want := range []string{`"release"`, "p3, p2", "--to"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("an empty history has nothing to roll back to", func(t *testing.T) {
		_, err := rollbackTarget(nil, "", "")
		if err == nil || !strings.Contains(err.Error(), "nothing to roll back to") {
			t.Errorf("err = %v, want an empty history refused", err)
		}
	})

	t.Run("the earliest promotion being live leaves nothing earlier", func(t *testing.T) {
		_, err := rollbackTarget([]*contractv1.PromotionHistoryEntry{entry("p1", "", true)}, "", "")
		if err == nil || !strings.Contains(err.Error(), "p1") {
			t.Errorf("err = %v, want the sole live promotion named as the earliest there is", err)
		}
	})

	t.Run("a history with nothing live is refused", func(t *testing.T) {
		history := []*contractv1.PromotionHistoryEntry{entry("p2", "", false), entry("p1", "", false)}
		_, err := rollbackTarget(history, "", "")
		if err == nil || !strings.Contains(err.Error(), "is live") {
			t.Errorf("err = %v, want a history with no live promotion refused", err)
		}
	})

	t.Run("the promotion before the live one is the default target", func(t *testing.T) {
		history := []*contractv1.PromotionHistoryEntry{entry("p3", "", false), entry("p2", "", true), entry("p1", "", false)}
		target, err := rollbackTarget(history, "", "")
		if err != nil {
			t.Fatalf("rollbackTarget err = %v", err)
		}
		if target.GetPromotionId() != "p1" {
			t.Errorf("target = %q, want the promotion before the live one", target.GetPromotionId())
		}
	})
}

func TestARollbackPassesOverAPromotionTakenBackAndRefusesToNameOne(t *testing.T) {
	history := []*contractv1.PromotionHistoryEntry{
		{Promotion: &contractv1.Promotion{PromotionId: "p3"}, Active: true},
		{Promotion: &contractv1.Promotion{PromotionId: "p2"}, Unpromoted: true},
		{Promotion: &contractv1.Promotion{PromotionId: "p1"}},
	}

	target, err := rollbackTarget(history, "", "")
	if err != nil {
		t.Fatalf("rollbackTarget err = %v", err)
	}
	if target.GetPromotionId() != "p1" {
		t.Errorf("target = %q, want p1: p2 was taken back and never served", target.GetPromotionId())
	}

	if _, err := rollbackTarget(history, "p2", ""); err == nil || !strings.Contains(err.Error(), "p2") || !strings.Contains(err.Error(), "never served") {
		t.Errorf("rollbackTarget(--to p2) err = %v, want p2 refused as a promotion that never served", err)
	}
}

func TestARollbackShowsTheWarningsTheProviderReturned(t *testing.T) {
	project := clitest.SetUpProject(t)
	kept := make([]router.Promotion, fake.KeptPromotions)
	for i := range kept {
		kept[i] = router.Promotion{PromotionID: fmt.Sprintf("p%02d", i), Ts: int64(i + 1), Builds: map[string]string{"web": fmt.Sprintf("build-%02d~fp%02d", i, i)}}
	}
	recordPromotions(t, project, kept...)
	const warned = "the stack of an old build is still provisioned"
	project.Provider.FakeStacks().RefuseNextDestroy(errors.New(warned))
	invocation := clitest.NewInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, warned) || !strings.Contains(out, "Rolled back to promotion p18") {
		t.Errorf("stdout = %q, want the rollback reported with the provider's warning %q", out, warned)
	}
}

var propagationCases = []struct {
	name        string
	propagation router.Propagation
	want        string
	other       []string
}{
	{
		name:  "instant",
		other: []string{"propagates"},
	},
	{
		name:        "published",
		propagation: router.Propagation{Typical: 5 * time.Second, Published: true},
		want:        "propagates within ~5 s",
		other:       []string{"typical, not guaranteed"},
	},
	{
		name:        "unpublished",
		propagation: router.Propagation{Typical: 5 * time.Second},
		want:        "propagates in ~5 s (typical, not guaranteed)",
		other:       []string{"propagates within"},
	},
}

func TestPropagationOnTheRollbackPromotionLine(t *testing.T) {
	for _, tc := range propagationCases {
		t.Run(tc.name, func(t *testing.T) {
			project := promotedTwice(t)
			project.Provider.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Propagates(tc.propagation)
			invocation := clitest.NewInvocation()

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(invocation, &stdout)
			if err := runRollback(context.Background(), invocation, project.Root, rollbackOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runRollback err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			out := stdout.String()
			line := ""
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "Rolled back to promotion") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("stdout = %q, want a rolled-back line", out)
			}
			assertPropagationNote(t, line, tc.want, tc.other)
		})
	}
}

func assertPropagationNote(t *testing.T, out, want string, absent []string) {
	t.Helper()
	if want != "" && !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to contain %q", out, want)
	}
	for _, unwanted := range absent {
		if strings.Contains(out, unwanted) {
			t.Errorf("output = %q, want no %q", out, unwanted)
		}
	}
}
