package cli

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestRunDeploymentsLs(t *testing.T) {
	t.Run("it renders promotions newest first with the active marker", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runPromotionsLs(context.Background(), deps, root, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
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

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("it shows each app's shipped identity under an aligned column", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runPromotionsLs(context.Background(), deps, root, &stdout, &stderr); err != nil {
			t.Fatalf("runPromotionsLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
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

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := runPromotionsLs(context.Background(), deps, root, &stdout, &stderr)
		if err == nil {
			t.Fatal("runPromotionsLs err = nil, want a tier-mismatch error")
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

func TestRunDeploymentsPrune(t *testing.T) {
	t.Run("it reports the reclaimed and the kept promotions", func(t *testing.T) {
		root, sockPath := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := runPromotionsPrune(context.Background(), deps, root, 10); err != nil {
			t.Fatalf("runPromotionsPrune err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Reclaimed promotion promo-1, kept 1") {
			t.Errorf("stdout = %q, want it to report the reclaimed promotion and the kept count", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("it refuses on preview infrastructure", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := runPromotionsPrune(context.Background(), deps, root, 10)
		if err == nil {
			t.Fatal("runPromotionsPrune err = nil, want a tier-mismatch failure")
		}
		out := stdout.String()
		if !strings.Contains(out, "this command needs production infrastructure") {
			t.Errorf("stdout = %q, want the concrete tier-mismatch message", out)
		}
		if strings.Contains(out, "Reclaimed") {
			t.Errorf("stdout = %q, want no prune to have been driven against preview infra", out)
		}
	})
}

func TestListingDeploymentsSaysWhoItActsAsInTheCheckPhaseAndPrintsItsTableBesideTheStream(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	deps := newTestDeps()
	clitest.SetLoggedIn(&deps)
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stream)
	if err := runPromotionsLs(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("runPromotionsLs err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	evs := runEvents(t, stream.String())
	if len(evs) == 0 {
		t.Fatalf("the run reported nothing on its stream; stdout=%s", stdout.String())
	}
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < 0 || evs[identity].GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("the listing never said who it acts as in the check phase: %s", stream.String())
	}
	if result := evs[len(evs)-1].GetResult(); !result.GetSuccess() {
		t.Errorf("result = %v, want the listing's run to succeed", result)
	}
	if !strings.Contains(stdout.String(), "promo-2") || strings.Contains(stream.String(), "promo-2") {
		t.Errorf("stdout = %q, stream = %q: want the table on stdout and not on the stream", stdout.String(), stream.String())
	}
}

func TestPruningReportsWhatItReclaimedThroughTheRunsEvents(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	deps := newTestDeps()
	clitest.SetLoggedIn(&deps)
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

	var stream bytes.Buffer
	clitest.AttachTerminalSink(deps, &stream)
	if err := runPromotionsPrune(context.Background(), deps, root, 10); err != nil {
		t.Fatalf("runPromotionsPrune err = %v; stream=%s", err, stream.String())
	}

	evs := runEvents(t, stream.String())
	if len(evs) == 0 {
		t.Fatal("the run reported nothing on its stream")
	}
	if !slices.ContainsFunc(evs, func(ev *streamv1.RunEvent) bool {
		return strings.Contains(ev.GetMessage(), "Reclaimed promotion promo-1")
	}) {
		t.Errorf("the stream never said what was reclaimed: %s", stream.String())
	}
	if result := evs[len(evs)-1].GetResult(); !result.GetSuccess() || result.GetHeadline() != "Pruned the production promotions of "+clitest.FixtureSlug+" down to the newest 10" {
		t.Errorf("result = %v, want the run to end reporting the prune", result)
	}
}
