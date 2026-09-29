package destroy

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestDestroyingPreviewTakesTheWholePreviewFootprintOnceConsented(t *testing.T) {
	t.Run("--yes skips the terminal check and the typed name", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyPreviewProject(context.Background(), invocation, root, true, false, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`ENTIRE preview footprint of project "test-app"`,
			"fronted by the direct edge",
			"– fake/test-app--pr-1--infra  [infra]  — databases and buckets, INCLUDING ALL DATA",
			"– fake/test-app--pr-2--infra  [infra]",
			"– fake/test-app--pr-1--web--b1  [web]",
			"– direct/edge",
			"    – test-app  Fake::Front",
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
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: "zone",
};
`)
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyPreviewProject(context.Background(), invocation, root, true, false, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s", err, stdout.String())
		}
		if out := stdout.String(); !strings.Contains(out, "DESTROY PROJECT project=test-app dns=zone") {
			t.Errorf("stdout = %q, want the dns descriptor on the teardown request", out)
		}
	})

	t.Run("--dry prints the plan, tears nothing down, and needs no terminal", func(t *testing.T) {
		root, journal := clitest.SetUpEdgeFixture(t, "")
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyPreviewProject(context.Background(), invocation, root, true, true, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s", err, stdout.String())
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
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyPreviewProject(context.Background(), invocation, root, false, false, &stdout, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyPreviewProject without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "--yes") {
			t.Errorf("err = %v, want the no-TTY refusal to point at --yes", err)
		}
	})
}

func TestDestroyingProductionShowsThePlanAndTakesTheProjectNameBeforeDestroying(t *testing.T) {
	t.Run("it refuses without a terminal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyProduction without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), consent.BypassEnv) {
			t.Errorf("err = %v, want the no-TTY refusal to name %s, the only way production destroys unattended", err, consent.BypassEnv)
		}
	})

	t.Run("the project name gets past the terminal requirement and says so", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader(""))
		if err != nil && strings.Contains(err.Error(), "needs a terminal") {
			t.Errorf("err = %v, want the bypass to get past the TTY requirement", err)
		}
		if strings.Contains(stdout.String(), "Type the project name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed-name confirmation", stdout.String())
		}
		if want := "WARN  [check] " + consent.BypassEnv + "=test-app: destroying production without confirmation"; !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want the run to warn %q so an unconfirmed destroy is never silent", stdout.String(), want)
		}
	})

	t.Run("it renders the plan the provider sent, keeps collapsed into the tally", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the direct edge",
			"– direct/edge",
			"    – disable, then delete E1test-app  Fake::Front (slow)",
			"– fake/test-app--infra  [infra]",
			"– fake/test-app--web--b1  [web]",
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
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		if strings.Contains(out, "consented=none") {
			t.Fatalf("the destroy reached the provider with no plan behind it; got:\n%s", out)
		}
		for _, want := range []string{"direct/edge", "fake/test-app--infra", "fake/test-app--web--b1"} {
			if !strings.Contains(out, "consented=") || !strings.Contains(consentedLine(out), want) {
				t.Errorf("the destroy sent %q, want the plan it showed to name %q", consentedLine(out), want)
			}
		}
	})

	t.Run("--dry prints the plan, destroys nothing, and needs no terminal", func(t *testing.T) {
		root, journal := clitest.SetUpEdgeFixture(t, "")
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, root, false, true, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
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
		invocation := clitest.NewInvocation()
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeEmptyRemovalPlanEnvVar, "1")
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
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
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDestroyProduction err = nil, want an ambient %s=1 refused; stdout=%s", consent.BypassEnv, stdout.String())
		}
		if !strings.Contains(err.Error(), consent.BypassEnv) || !strings.Contains(err.Error(), "test-app") {
			t.Errorf("err = %v, want it to name %s and the project", err, consent.BypassEnv)
		}
	})

	t.Run("an unset bypass is not a bypass", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), consent.BypassEnv) {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})
}

func TestDestroyingProductionAsksForTheProjectNameWhileTheRunIsHeldAfterThePlanItShows(t *testing.T) {
	root, _ := clitest.SetUpDeployFixture(t)
	invocation := clitest.NewInvocation()
	invocation.StdinIsTerminal = func(io.Reader) bool { return true }
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}
	t.Setenv(clitest.FakeInfraTierEnvVar, "production")
	t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

	var stream, stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stream)
	if err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader("test-app\n")); err != nil {
		t.Fatalf("runDestroyProduction err = %v; stream=%s stdout=%s", err, stream.String(), stdout.String())
	}

	evs := clitest.RunEvents(t, stream.String())
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
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() || result.GetHeadline() != "Destroyed project test-app" {
		t.Errorf("result = %v, want the run to end reporting the destroyed project", result)
	}
}

func TestDestroyNeedsATier(t *testing.T) {
	command := NewCommand(clitest.NewInvocation())
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	t.Cleanup(func() { command.SetOut(nil); command.SetErr(nil) })

	if err := command.RunE(command, nil); err == nil {
		t.Fatal("bare destroy err = nil, want destroy without a tier to be a failure")
	}
	for _, want := range []string{"production", "preview"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want the help to list %q", out.String(), want)
		}
	}
}

func TestDestroyNamesATierItDoesNotKnow(t *testing.T) {
	command := NewCommand(clitest.NewInvocation())
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	t.Cleanup(func() { command.SetOut(nil); command.SetErr(nil) })

	err := command.RunE(command, []string{"foo"})
	if err == nil {
		t.Fatal("destroy foo err = nil, want a tier it does not know to be a failure")
	}
	if err.Error() != `the tier to destroy is production or preview, not "foo"` {
		t.Errorf("err = %v, want it to name the value typed", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want no help dump when the tier is named but wrong", out.String())
	}
}

func TestDestroyTakesTheTierAsASubcommandOrItsAlias(t *testing.T) {
	command := NewCommand(clitest.NewInvocation())
	for typed, want := range map[string]string{
		"production": "production",
		"prod":       "production",
		"preview":    "preview",
	} {
		found, _, err := command.Find([]string{typed})
		if err != nil {
			t.Fatalf("Find(%q) err = %v", typed, err)
		}
		if found.Name() != want {
			t.Errorf("Find(%q) = %q, want %q", typed, found.Name(), want)
		}
	}

	production, _, _ := command.Find([]string{"production"})
	preview, _, _ := command.Find([]string{"preview"})
	for _, cmd := range []*cobra.Command{production, preview} {
		if cmd.Flags().Lookup("dry") == nil {
			t.Errorf("destroy %s has no --dry; every destructive command previews", cmd.Name())
		}
		yes := cmd.Flags().Lookup("yes")
		if yes == nil {
			t.Fatalf("destroy %s has no --yes; one flag grants consent on every command", cmd.Name())
		}
		if yes.Usage != commands.YesUsage {
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

func TestDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
		planned     string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= ", "direct"},
		{"a declared api-gateway edge names it", "  edge: \"api-gateway\",\n", "kind=api-gateway", "api-gateway"},
		{"a declared relay edge names it", "  edge: \"relay\",\n", "kind=relay", "relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal := clitest.SetUpEdgeFixture(t, tc.declaration)
			invocation := clitest.NewInvocation()
			t.Setenv(clitest.FakeInfraTierEnvVar, "production")
			t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
			t.Setenv(consent.BypassEnv, "test-app")

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(invocation, &stdout)
			if err := runDestroyProduction(context.Background(), invocation, root, false, false, &stdout, strings.NewReader("")); err != nil {
				t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
			if len(got) != 2 {
				t.Fatalf("destroy reached the provider %d times, want the plan and the teardown: %v", len(got), got)
			}
			for _, line := range got {
				if !strings.Contains(line, tc.want) {
					t.Errorf("provider saw %q, want %q", line, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the "+tc.planned+" edge") {
				t.Errorf("stdout = %q, want the plan to name the edge it planned", stdout.String())
			}
		})
	}
}
