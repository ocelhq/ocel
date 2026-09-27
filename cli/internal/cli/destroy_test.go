package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestRunDestroyPreviewProject(t *testing.T) {
	t.Run("--yes skips the terminal check and the typed name", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyPreviewProject(context.Background(), deps, root, true, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`ENTIRE preview footprint of project "test-app"`,
			"fronted by the cloudfront edge",
			"– aws/test-app--pr-1--infra  [infra]  — databases and buckets, INCLUDING ALL DATA",
			"– aws/test-app--pr-2--infra  [infra]",
			"– aws/test-app--pr-1--web--b1  [web]",
			"– cloudfront/edge",
			"    – test-app  AWS::CloudFront::Distribution",
			"every preview variable value",
			"The account-level preview bootstrap is left intact. This cannot be undone.",
			"4 to delete, 1 unchanged.",
			"DESTROY PROJECT project=test-app dns= tier=TIER_PREVIEW",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "bootstrap-scoped") {
			t.Errorf("stdout spent a row on something staying put:\n%s", out)
		}
		if strings.Contains(out, "Type the project name") {
			t.Errorf("stdout = %q, want --yes to skip the typed-name confirmation", out)
		}
	})

	t.Run("the dns descriptor rides along so the teardown can delete what it wrote", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { aws: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: "route53",
};
`)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyPreviewProject(context.Background(), deps, root, true, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "DESTROY PROJECT project=test-app dns=route53") {
			t.Errorf("stdout = %q, want the dns descriptor on the teardown request", out)
		}
	})

	t.Run("--dry prints the plan, tears nothing down, and needs no terminal", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyPreviewProject(context.Background(), deps, root, true, true, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `ENTIRE preview footprint of project "test-app"`) {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to destroy it", out)
		}
		if strings.Contains(out, "DESTROY PROJECT") {
			t.Errorf("stdout = %q, want --dry to beat --yes and destroy nothing", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the plan alone", got)
		}
	})

	t.Run("without --yes it refuses without a terminal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		err := runDestroyPreviewProject(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyPreviewProject without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want the no-TTY refusal to point at --yes", err)
		}
	})
}

func TestRunDestroy(t *testing.T) {
	t.Run("it refuses without a terminal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyProduction without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), consent.BypassEnv) {
			t.Errorf("err = %v, want the no-TTY refusal to name %s, the only way production destroys unattended", err, consent.BypassEnv)
		}
	})

	t.Run("the project name gets past the terminal requirement and says so", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader(""))
		if err != nil && strings.Contains(err.Error(), "needs a terminal") {
			t.Errorf("err = %v, want the bypass to get past the TTY requirement", err)
		}
		if strings.Contains(stdout.String(), "Type the project name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed-name confirmation", stdout.String())
		}
		if !strings.Contains(stderr.String(), consent.BypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed destroy is never silent", stderr.String(), consent.BypassEnv)
		}
	})

	t.Run("it renders the plan the provider sent, keeps collapsed into the tally", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the cloudfront edge",
			"– cloudfront/edge",
			"    – disable, then delete E1test-app  AWS::CloudFront::Distribution (slow)",
			"– aws/test-app--infra  [infra]",
			"– aws/test-app--web--b1  [web]",
			"4 to delete, 1 unchanged.",
			"DESTROY PROJECT project=test-app",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "you pinned this certificate") {
			t.Errorf("stdout spent a row on a certificate nothing touches; got:\n%s", out)
		}
	})

	t.Run("the destroy sends the plan the human consented to", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if strings.Contains(out, "consented=none") {
			t.Fatalf("the destroy reached the provider with no plan behind it; got:\n%s", out)
		}
		for _, want := range []string{"cloudfront/edge", "aws/test-app--infra", "aws/test-app--web--b1"} {
			if !strings.Contains(out, "consented=") || !strings.Contains(consentedLine(out), want) {
				t.Errorf("the destroy sent %q, want the plan it showed to name %q", consentedLine(out), want)
			}
		}
	})

	t.Run("--dry prints the plan, destroys nothing, and needs no terminal", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyProduction(context.Background(), deps, root, false, true, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `This will permanently destroy production project "test-app"`) {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to destroy it", out)
		}
		if strings.Contains(out, "DESTROY PROJECT") {
			t.Errorf("stdout = %q, want --dry to destroy nothing", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the plan alone", got)
		}
	})

	t.Run("an empty plan destroys nothing and never asks for the project name", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeEmptyRemovalPlanEnvVar, "1")
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		if err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "✓ Nothing to destroy: test-app has nothing in production in ") {
			t.Errorf("stdout = %q, want it to say nothing of test-app was in production to destroy", out)
		}
		for _, unwanted := range []string{"This will permanently destroy", "DESTROY PROJECT"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("stdout = %q, want no %q: an empty plan must stop before the plan is rendered", out, unwanted)
			}
		}
	})

	t.Run("a value that is not this project's name is refused without a terminal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(consent.BypassEnv, "1")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDestroyProduction err = nil, want an ambient %s=1 refused; stdout=%s", consent.BypassEnv, stdout.String())
		}
		if !strings.Contains(err.Error(), consent.BypassEnv) || !strings.Contains(err.Error(), "test-app") {
			t.Errorf("err = %v, want it to name %s and the project", err, consent.BypassEnv)
		}
	})

	t.Run("an unset bypass is not a bypass", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newTestDeps()
		clitest.SetLoggedIn(&deps)
		t.Setenv(consent.BypassEnv, "")

		var stdout, stderr bytes.Buffer
		deps.AttachTerminalSink(&stdout)
		err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), consent.BypassEnv) {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})
}

func TestDestroyingProductionAsksForTheProjectNameWhileTheRunIsHeldAfterThePlanItShows(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	deps := newTestDeps()
	clitest.SetLoggedIn(&deps)
	deps.StdinIsTerminal = func(io.Reader) bool { return true }
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

	var stream, stdout, stderr bytes.Buffer
	deps.AttachTerminalSink(&stream)
	if err := runDestroyProduction(context.Background(), deps, root, false, false, &stdout, &stderr, strings.NewReader("test-app\n")); err != nil {
		t.Fatalf("runDestroyProduction err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	evs := runEvents(t, stream.String())
	shown := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetPlan() != nil })
	if shown < 0 || evs[shown].GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("the destroy plan was not shown in the plan phase: %s", stream.String())
	}
	waiting := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil })
	if waiting < shown {
		t.Fatalf("held at event %d, plan shown at %d: want the run held to ask once the plan is shown: %s", waiting, shown, stream.String())
	}
	resumed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if resumed < waiting || evs[resumed].GetResumed().GetReason() != "answered" {
		t.Fatalf("resumed at event %d, held at %d: want the run resumed once answered: %s", resumed, waiting, stream.String())
	}
	if result := evs[len(evs)-1].GetResult(); !result.GetSuccess() || result.GetHeadline() != "Destroyed project test-app" {
		t.Errorf("result = %v, want the run to end reporting the destroyed project", result)
	}
}

func TestDestroyNeedsAClass(t *testing.T) {
	var out bytes.Buffer
	destroyCmd.SetOut(&out)
	destroyCmd.SetErr(&out)
	t.Cleanup(func() { destroyCmd.SetOut(nil); destroyCmd.SetErr(nil) })

	if err := destroyCmd.RunE(destroyCmd, nil); err == nil {
		t.Fatal("bare destroy err = nil, want destroy without a class to be a failure")
	}
	for _, want := range []string{"production", "preview"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want the help to list %q", out.String(), want)
		}
	}
}

func TestDestroyNamesAClassItDoesNotKnow(t *testing.T) {
	var out bytes.Buffer
	destroyCmd.SetOut(&out)
	destroyCmd.SetErr(&out)
	t.Cleanup(func() { destroyCmd.SetOut(nil); destroyCmd.SetErr(nil) })

	err := destroyCmd.RunE(destroyCmd, []string{"foo"})
	if err == nil {
		t.Fatal("destroy foo err = nil, want a class it does not know to be a failure")
	}
	if err.Error() != `the class to destroy is production or preview, not "foo"` {
		t.Errorf("err = %v, want it to name the value typed", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want no help dump when the class is named but wrong", out.String())
	}
}

func TestDestroyClassCommands(t *testing.T) {
	for typed, want := range map[string]string{
		"production": "production",
		"prod":       "production",
		"preview":    "preview",
	} {
		found, _, err := destroyCmd.Find([]string{typed})
		if err != nil {
			t.Fatalf("Find(%q) err = %v", typed, err)
		}
		if found.Name() != want {
			t.Errorf("Find(%q) = %q, want %q", typed, found.Name(), want)
		}
	}

	production, _, _ := destroyCmd.Find([]string{"production"})
	preview, _, _ := destroyCmd.Find([]string{"preview"})
	for _, cmd := range []*cobra.Command{production, preview} {
		if cmd.Flags().Lookup("dry") == nil {
			t.Errorf("destroy %s has no --dry; every destructive command previews", cmd.Name())
		}
		yes := cmd.Flags().Lookup("yes")
		if yes == nil {
			t.Fatalf("destroy %s has no --yes; one flag grants consent on every command", cmd.Name())
		}
		if yes.Usage != cmddeps.YesUsage {
			t.Errorf("destroy %s --yes usage = %q, want the one line every command shows", cmd.Name(), yes.Usage)
		}
	}
}

func consentedLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "consented=") {
			return line
		}
	}
	return ""
}
