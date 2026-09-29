package promotions

import (
	"bytes"
	"context"
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

func promotedTwice(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	recordPromotions(t, project, firstTwoPromotions()...)
	return project
}

func promotedThrice(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	recordPromotions(t, project, append(firstTwoPromotions(),
		router.Promotion{PromotionID: "promo-3", Ts: 3, Builds: map[string]string{"web": "build-3~fp4"}})...)
	return project
}

func firstTwoPromotions() []router.Promotion {
	return []router.Promotion{
		{PromotionID: "promo-1", Ts: 1, Tag: "v1.0.0", Builds: map[string]string{"web": "build-1~fp1"}},
		{PromotionID: "promo-2", Ts: 2, Builds: map[string]string{"web": "build-2~fp2", "admin": "build-2~fp3"},
			Propagation: &router.Propagation{Typical: 5 * time.Second}},
	}
}

func recordPromotions(t *testing.T, project clitest.FakeProject, promotions ...router.Promotion) {
	t.Helper()
	clitest.RecordEdgeStack(t, project, environment.TierProduction, fake.KindRelay)
	releases := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug)
	replaces := ""
	for _, promotion := range promotions {
		for app, build := range promotion.Builds {
			if err := releases.PutStaged(context.Background(), router.DeploymentRecord{App: app, Build: build}); err != nil {
				t.Fatalf("stage %s=%s: %v", app, build, err)
			}
		}
		if _, err := releases.Promote(context.Background(), promotion, "", replaces); err != nil {
			t.Fatalf("promote %s: %v", promotion.PromotionID, err)
		}
		replaces = promotion.PromotionID
	}
}

func activePromotionID(t *testing.T, project clitest.FakeProject) string {
	t.Helper()
	active, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).ActivePromotionID(context.Background(), "")
	if err != nil {
		t.Fatalf("read the active promotion: %v", err)
	}
	return active
}

func bootstrappedOnlyForPreview(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	if err := project.Provider.FakeBootstrap().Remove(context.Background(), environment.TierProduction, nil); err != nil {
		t.Fatalf("remove the production bootstrap: %v", err)
	}
	clitest.Bootstrap(t, project.Provider, environment.TierPreview)
}

func TestDeploymentsListRendersPromotionsNewestFirstWithTheActiveOne(t *testing.T) {
	t.Run("it renders promotions newest first with the active marker", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runPromotionsList(context.Background(), invocation, project.Root, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, sub := range []string{"ID", "TAG", "CREATED", "STATUS", "promo-2", "promo-1", "v1.0.0", "active"} {
			if !strings.Contains(out, sub) {
				t.Errorf("stdout = %q, want it to contain %q", out, sub)
			}
		}

		promo2Idx := strings.Index(out, "promo-2")
		promo1Idx := strings.Index(out, "promo-1")
		if promo2Idx == -1 || promo1Idx == -1 || promo2Idx > promo1Idx {
			t.Errorf("stdout = %q, want promo-2 (newest) listed before promo-1", out)
		}
	})

	t.Run("it shows each app's shipped identity under an aligned column", func(t *testing.T) {
		project := promotedTwice(t)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runPromotionsList(context.Background(), invocation, project.Root, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, sub := range []string{"DEPLOYED", "admin=build-2", "web=build-2~fp2", "web=build-1"} {
			if !strings.Contains(out, sub) {
				t.Errorf("stdout = %q, want it to contain %q", out, sub)
			}
		}

		banner := strings.Index(out, "ocel  dev  test-app › production\n")
		cut := strings.Index(out, "ID  ")
		if banner < 0 || cut < banner {
			t.Fatalf("stdout = %q, want the identity banner above the table", out)
		}
		table := out[cut:]
		lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("stdout = %q, want a header and two rows", out)
		}
		if a, b := runeIndex(lines[0], "DEPLOYED"), runeIndex(lines[1], "admin="); a != b {
			t.Errorf("DEPLOYED column starts at %d in the header and %d in the row:\n%s", a, b, out)
		}
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		project := promotedTwice(t)
		bootstrappedOnlyForPreview(t, project)
		invocation := clitest.NewInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runPromotionsList(context.Background(), invocation, project.Root, &stdout, &stderr)
		if err == nil {
			t.Fatal("runPromotionsList err = nil, want a tier-mismatch error")
		}
		if out := stdout.String(); !strings.Contains(out, "this command needs production infrastructure") {
			t.Errorf("stdout = %q, want the concrete tier-mismatch message", out)
		}
	})
}

func runeIndex(line, substr string) int {
	at := strings.Index(line, substr)
	if at < 0 {
		return at
	}
	return len([]rune(line[:at]))
}

func TestDeploymentsPruneReclaimsOldPromotionsOnceConsented(t *testing.T) {
	t.Run("it reports the reclaimed and the kept promotions", func(t *testing.T) {
		project := promotedThrice(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1, yes: true}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runPromotionsPrune err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Reclaimed promotion promo-1, kept 2") {
			t.Errorf("stdout = %q, want it to report the reclaimed promotion and the kept count", out)
		}
		history, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).History(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 || history[0].PromotionID != "promo-3" || history[1].PromotionID != "promo-2" {
			t.Errorf("the ledger holds %+v after the prune, want promo-3 and the promo-2 it replaced", history)
		}
	})

	t.Run("it refuses without a terminal or --yes and reclaims nothing", func(t *testing.T) {
		project := promotedThrice(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1}, &stdout, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "pass --yes") {
			t.Fatalf("runPromotionsPrune without a terminal err = %v, want a refusal naming --yes", err)
		}
		assertKeptEveryPromotion(t, project)
	})

	t.Run("a declined confirmation reclaims nothing", func(t *testing.T) {
		project := promotedThrice(t)
		invocation := clitest.NewInvocation()
		invocation.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1}, &stdout, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runPromotionsPrune err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Not confirmed, so this run changes nothing") {
			t.Errorf("stdout = %q, want a declined confirmation to say so", out)
		}
		if strings.Contains(out, "Reclaimed") {
			t.Errorf("stdout = %q, want nothing reclaimed behind a declined confirmation", out)
		}
		assertKeptEveryPromotion(t, project)
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		project := promotedThrice(t)
		bootstrappedOnlyForPreview(t, project)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1, yes: true}, &stdout, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPromotionsPrune err = nil, want a tier-mismatch failure")
		}
		if out := stdout.String(); !strings.Contains(out, "this command needs production infrastructure") {
			t.Errorf("stdout = %q, want the concrete tier-mismatch message", out)
		}
		assertKeptEveryPromotion(t, project)
	})
}

func assertKeptEveryPromotion(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	history, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Errorf("the ledger holds %+v, want every promotion kept", history)
	}
}

func TestListingDeploymentsSaysWhoItActsAsInTheCheckPhaseAndPrintsItsTableBesideTheStream(t *testing.T) {
	project := promotedTwice(t)
	invocation := clitest.NewInvocation()
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stream)
	if err := runPromotionsList(context.Background(), invocation, project.Root, &stdout, &stderr); err != nil {
		t.Fatalf("runPromotionsList err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	evs := clitest.RunEvents(t, stream.String())
	if len(evs) == 0 {
		t.Fatalf("the run reported nothing on its stream; stdout=%s", stdout.String())
	}
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < 0 || evs[identity].GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("the listing never said who it acts as in the check phase: %s", stream.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() {
		t.Errorf("result = %v, want the listing's run to succeed", result)
	}
	if !strings.Contains(stdout.String(), "promo-2") || strings.Contains(stream.String(), "promo-2") {
		t.Errorf("stdout = %q, stream = %q: want the table on stdout and not on the stream", stdout.String(), stream.String())
	}
}

func TestPruningReportsWhatItReclaimedThroughTheRunsEvents(t *testing.T) {
	project := promotedThrice(t)
	invocation := clitest.NewInvocation()
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}

	var stream bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stream)
	if err := runPromotionsPrune(context.Background(), invocation, project.Root, pruneOptions{keep: 1, yes: true}, &stream, strings.NewReader("")); err != nil {
		t.Fatalf("runPromotionsPrune err = %v; stream=%s", err, stream.String())
	}

	evs := clitest.RunEvents(t, stream.String())
	if len(evs) == 0 {
		t.Fatal("the run reported nothing on its stream")
	}
	if !slices.ContainsFunc(evs, func(ev *streamv1.RunEvent) bool {
		return strings.Contains(ev.GetOperation().GetMessage(), "Reclaimed promotion promo-1")
	}) {
		t.Errorf("the stream never said what was reclaimed: %s", stream.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() || result.GetHeadline() != "Pruned the production promotions of "+clitest.FixtureSlug+" down to the newest 1" {
		t.Errorf("result = %v, want the run to end reporting the prune", result)
	}
}

func TestTheDeploymentsListMarksAPromotionTakenBack(t *testing.T) {
	var stdout bytes.Buffer
	renderPromotions(&stdout, []*contractv1.PromotionHistoryEntry{
		{Promotion: &contractv1.Promotion{PromotionId: "p2"}, Unpromoted: true},
		{Promotion: &contractv1.Promotion{PromotionId: "p1"}, Active: true},
	})

	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, "p2") && !strings.HasSuffix(strings.TrimSpace(line), "unpromoted") {
			t.Errorf("the row of p2 reads %q, want it marked unpromoted", line)
		}
	}
	if !strings.Contains(stdout.String(), "unpromoted") {
		t.Errorf("stdout = %q, want p2 marked unpromoted", stdout.String())
	}
}

func TestACommandThatReadsTheBootstrapNamesTheFeatureItLacks(t *testing.T) {
	project := promotedTwice(t)
	project.Provider.FakeBootstrap().DescribeAbsent(fake.FeatureCache, fake.FeatureImages)
	invocation := clitest.NewInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	err := runPromotionsList(context.Background(), invocation, project.Root, &stdout, &stderr)
	if err == nil {
		t.Fatal("a command reading a bootstrap that lacks a feature this project needs ran on regardless")
	}
	if out := stdout.String(); !strings.Contains(out, "ocel bootstrap production --features "+fake.FeatureCache+","+fake.FeatureImages) {
		t.Errorf("refusal = %q, want the literal command to run", out)
	}
}

func TestPropagationIsAbsentFromThePromotionList(t *testing.T) {
	project := promotedTwice(t)
	invocation := clitest.NewInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	if err := runPromotionsList(context.Background(), invocation, project.Root, &stdout, &stderr); err != nil {
		t.Fatalf("runPromotionsList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if strings.Contains(stdout.String(), "propagates") {
		t.Errorf("stdout = %q, want the flip note only on a promotion line", stdout.String())
	}
}
