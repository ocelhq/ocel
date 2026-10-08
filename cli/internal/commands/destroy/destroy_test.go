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

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func deployedToPreview(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview, fake.FeatureCache, fake.FeatureImages)
	clitest.RecordStacks(t, project, environment.TierPreview,
		naming.InfraStack("pr-1"), naming.InfraStack("pr-2"), naming.AppStack("pr-1", "web", naming.NewReleaseToken("b1", "")))
	clitest.RecordEdgeStack(t, project, environment.TierPreview, fake.KindDirect)
	return project
}

func deployedToProduction(t *testing.T) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.RecordStacks(t, project, environment.TierProduction,
		naming.InfraStack(stackrecords.ProductionEnv), naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("b1", "")))
	clitest.RecordEdgeStack(t, project, environment.TierProduction, fake.KindDirect)
	return project
}

func removals(t *testing.T, project clitest.FakeProject) (planned, removed []*contractv1.ProjectRequest) {
	t.Helper()
	planned = clitest.RequestsTo[*contractv1.ProjectRequest](t, project.Requests, contractv1connect.ProviderServicePlanRemoveProjectProcedure)
	removed = clitest.RequestsTo[*contractv1.ProjectRequest](t, project.Requests, contractv1connect.ProviderServiceRemoveProjectProcedure)
	return planned, removed
}

func recordedStacks(t *testing.T, project clitest.FakeProject, tier environment.Tier) []stackrecords.NamedStack {
	t.Helper()
	recorded, err := stackrecords.List(context.Background(), project.Provider.KeyValues(), tier, clitest.FixtureSlug)
	if err != nil {
		t.Fatalf("list the recorded stacks: %v", err)
	}
	return recorded
}

func TestDestroyingPreviewTakesTheWholePreviewFootprintOnceConsented(t *testing.T) {
	t.Run("--yes skips the terminal check and the typed name", func(t *testing.T) {
		project := deployedToPreview(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyPreviewProject(context.Background(), invocation, project.Root, true, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`ENTIRE preview footprint of project "test-app"`,
			"fronted by the direct edge",
			"– fake/pr-1--infra  [pr-1]",
			"– fake/pr-2--infra  [pr-2]",
			"– fake/pr-1--web--",
			"– direct/edge",
			"    – test-app-preview  Fake::EdgeStack",
			"every preview variable value",
			"The account-level preview bootstrap is left intact. This cannot be undone.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "Type the project name") {
			t.Errorf("stdout = %q, want --yes to skip the typed-name confirmation", out)
		}
		_, removed := removals(t, project)
		if len(removed) != 1 || removed[0].GetSlug() != "test-app" || removed[0].GetEnvironment().GetTier() != environmentv1.Tier_TIER_PREVIEW {
			t.Fatalf("the provider was asked to remove %v, want test-app's preview footprint", removed)
		}
		if left := recordedStacks(t, project, environment.TierPreview); len(left) != 0 {
			t.Errorf("the provider still records %v, want every preview stack destroyed", left)
		}
	})

	t.Run("the dns descriptor rides along so the teardown can delete what it wrote", func(t *testing.T) {
		project := deployedToPreview(t)
		clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: { dns: "zone" } },
  domains: { preview: "*.preview.acme.com" },
};
`)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyPreviewProject(context.Background(), invocation, project.Root, true, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s", err, stdout.String())
		}
		_, removed := removals(t, project)
		if len(removed) != 1 || removed[0].GetEdge().GetDns().GetKind() != "zone" {
			t.Errorf("the provider was asked to remove %v, want the dns descriptor on the teardown request", removed)
		}
	})

	t.Run("--dry prints the plan, tears nothing down, and needs no terminal", func(t *testing.T) {
		project := deployedToPreview(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyPreviewProject(context.Background(), invocation, project.Root, true, true, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `ENTIRE preview footprint of project "test-app"`) {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to destroy it", out)
		}
		if planned, removed := removals(t, project); len(planned) != 1 || len(removed) != 0 {
			t.Errorf("the provider was asked for %d plans and %d removals, want the plan alone: --dry beats --yes", len(planned), len(removed))
		}
		if left := recordedStacks(t, project, environment.TierPreview); len(left) != 3 {
			t.Errorf("the provider records %v, want every preview stack left standing", left)
		}
	})

	t.Run("without --yes it refuses without a terminal", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyPreviewProject(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader(""))
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
		project := clitest.SetUpProject(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyProduction without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), consent.BypassEnv) {
			t.Errorf("err = %v, want the no-TTY refusal to name %s, the remedy beside --yes", err, consent.BypassEnv)
		}
		if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeConfirmationRequired || got.GetHint() != "--yes" {
			t.Errorf("run error = %v, want confirmation_required with the hint --yes", got)
		}
	})

	t.Run("--yes alone destroys production without a terminal", func(t *testing.T) {
		project := deployedToProduction(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, project.Root, true, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction --yes err = %v; stdout=%s", err, stdout.String())
		}
		if left := recordedStacks(t, project, environment.TierProduction); len(left) != 0 {
			t.Errorf("stacks left = %v, want --yes to destroy production unattended", left)
		}
	})

	t.Run("the project name gets past the terminal requirement and says so", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader(""))
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

	t.Run("it renders the plan the provider sent and destroys what it showed", func(t *testing.T) {
		project := deployedToProduction(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the direct edge",
			"– direct/edge",
			"    – test-app-production  Fake::EdgeStack",
			"– fake/prod--infra  [prod]",
			"– fake/prod--web--",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "you pinned this certificate") {
			t.Errorf("stdout spent a row on a certificate nothing touches; got:\n%s", out)
		}
		if left := recordedStacks(t, project, environment.TierProduction); len(left) != 0 {
			t.Errorf("the provider still records %v, want every production stack destroyed", left)
		}
	})

	t.Run("the destroy sends the plan the human consented to", func(t *testing.T) {
		project := deployedToProduction(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "test-app")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
		}

		_, removed := removals(t, project)
		if len(removed) != 1 || len(removed[0].GetConsented().GetGroups()) == 0 {
			t.Fatalf("the destroy reached the provider as %v, want it to carry the plan behind it", removed)
		}
		var consented []string
		for _, group := range removed[0].GetConsented().GetGroups() {
			consented = append(consented, group.GetName())
		}
		for _, want := range []string{"direct/edge", "fake/prod--infra"} {
			if !slices.Contains(consented, want) {
				t.Errorf("the destroy consented to %v, want the plan it showed to name %q", consented, want)
			}
		}
	})

	t.Run("--dry prints the plan, destroys nothing, and needs no terminal", func(t *testing.T) {
		project := deployedToProduction(t)
		invocation := clitest.NewInvocation()

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDestroyProduction(context.Background(), invocation, project.Root, false, true, &stdout, io.Discard, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `This will permanently destroy production project "test-app"`) {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to destroy it", out)
		}
		if planned, removed := removals(t, project); len(planned) != 1 || len(removed) != 0 {
			t.Errorf("the provider was asked for %d plans and %d removals, want the plan alone", len(planned), len(removed))
		}
		if left := recordedStacks(t, project, environment.TierProduction); len(left) != 2 {
			t.Errorf("the provider records %v, want every production stack left standing", left)
		}
	})

	t.Run("a value that is not this project's name is refused without a terminal", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "1")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDestroyProduction err = nil, want an ambient %s=1 refused; stdout=%s", consent.BypassEnv, stdout.String())
		}
		if !strings.Contains(err.Error(), consent.BypassEnv) || !strings.Contains(err.Error(), "test-app") {
			t.Errorf("err = %v, want it to name %s and the project", err, consent.BypassEnv)
		}
		if got := clierror.NewRunError(err).GetCode(); got != clierror.CodeConfirmationBypassMismatch {
			t.Errorf("code = %q, want confirmation_bypass_mismatch", got)
		}
	})

	t.Run("an unset bypass is not a bypass", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := clitest.NewInvocation()
		t.Setenv(consent.BypassEnv, "")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), consent.BypassEnv) {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})
}

func TestDestroyingProductionAsksForTheProjectNameWhileTheRunIsHeldAfterThePlanItShows(t *testing.T) {
	project := deployedToProduction(t)
	invocation := clitest.NewInvocation()
	invocation.StdinIsTerminal = func(io.Reader) bool { return true }
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}

	var stream, stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stream)
	if err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader("test-app\n")); err != nil {
		t.Fatalf("runDestroyProduction err = %v; stream=%s stdout=%s", err, stream.String(), stdout.String())
	}

	evs := clitest.RunEvents(t, stream.String())
	shown := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetOperation().GetPlan() != nil })
	if shown < 0 || evs[shown].GetOperation().GetPhase() != progressv1.Phase_PHASE_PLAN {
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

func TestDestroySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
		planned     string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "", "relay"},
		{"a declared direct edge names it", "edge: \"direct\"", "direct", "direct"},
		{"a declared relay edge names it", "edge: \"relay\"", "relay", "relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := clitest.SetUpMonorepoProject(t, tc.declaration)
			invocation := clitest.NewInvocation()
			t.Setenv(consent.BypassEnv, "test-app")

			var stdout bytes.Buffer
			clitest.AttachTerminalSink(invocation, &stdout)
			if err := runDestroyProduction(context.Background(), invocation, project.Root, false, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
				t.Fatalf("runDestroyProduction err = %v; stdout=%s", err, stdout.String())
			}

			planned, removed := removals(t, project)
			if len(planned) != 1 || len(removed) != 1 {
				t.Fatalf("destroy asked for %d plans and %d removals, want the plan and the teardown", len(planned), len(removed))
			}
			for _, req := range []*contractv1.ProjectRequest{planned[0], removed[0]} {
				if got := req.GetEdge().GetKind(); got != tc.want {
					t.Errorf("provider was sent edge %q, want %q", got, tc.want)
				}
			}
			if !strings.Contains(stdout.String(), "fronted by the "+tc.planned+" edge") {
				t.Errorf("stdout = %q, want the plan to name the edge it planned", stdout.String())
			}
		})
	}
}

const registryConfig = `
export default {
  slug: "test-app",
  provider: { fake: {} },
  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },
};
`

func TestDestroyingSendsTheRegistryTheProjectNamesSoTheImagesItPushedGoWithIt(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	for name, destroy := range map[string]func(commands.Invocation, string, *bytes.Buffer) error{
		"production": func(invocation commands.Invocation, root string, stdout *bytes.Buffer) error {
			return runDestroyProduction(context.Background(), invocation, root, true, false, stdout, io.Discard, strings.NewReader(""))
		},
		"preview": func(invocation commands.Invocation, root string, stdout *bytes.Buffer) error {
			return runDestroyPreviewProject(context.Background(), invocation, root, true, false, stdout, io.Discard, strings.NewReader(""))
		},
	} {
		t.Run(name, func(t *testing.T) {
			deployed := deployedToProduction
			if name == "preview" {
				deployed = deployedToPreview
			}
			project := deployed(t)
			clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), registryConfig)

			invocation := clitest.NewInvocation()
			var stdout bytes.Buffer
			clitest.AttachTerminalSink(invocation, &stdout)
			if err := destroy(invocation, project.Root, &stdout); err != nil {
				t.Fatalf("destroy err = %v; stdout=%s", err, stdout.String())
			}

			_, removed := removals(t, project)
			if len(removed) != 1 {
				t.Fatalf("the provider was asked to remove %v, want one removal", removed)
			}
			registry := removed[0].GetProjectRegistry()
			if registry.GetServer() != "registry.example.com" || registry.GetUsername() != "acme-bot" || registry.GetPassword() != "hunter2" {
				t.Errorf("the removal named registry %q as %q with password %q, want the project's registry with its secret resolved: it is how the images a deploy pushed there are deleted",
					registry.GetServer(), registry.GetUsername(), registry.GetPassword())
			}
		})
	}
}

func TestDestroyingWhoseRegistryVariableIsUnsetStillDestroysAndSaysWhatItLeft(t *testing.T) {
	project := deployedToProduction(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), registryConfig)
	invocation := clitest.NewInvocation()

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	if err := runDestroyProduction(context.Background(), invocation, project.Root, true, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
		t.Fatalf("destroy err = %v; stdout=%s", err, stdout.String())
	}

	_, removed := removals(t, project)
	if len(removed) != 1 || removed[0].GetProjectRegistry() != nil {
		t.Fatalf("the provider was asked to remove %v, want the project removed with no registry: a token that is gone must not keep a project undeletable", removed)
	}
	for _, want := range []string{"OCEL_TEST_REGISTRY_TOKEN", "registry.example.com"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to say the images in the registry stay and name %q", stdout.String(), want)
		}
	}
}

func TestDestroyingAProjectThatNamesNoRegistrySendsNone(t *testing.T) {
	project := deployedToProduction(t)
	invocation := clitest.NewInvocation()

	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	if err := runDestroyProduction(context.Background(), invocation, project.Root, true, false, &stdout, io.Discard, strings.NewReader("")); err != nil {
		t.Fatalf("destroy err = %v; stdout=%s", err, stdout.String())
	}
	if _, removed := removals(t, project); len(removed) != 1 || removed[0].GetProjectRegistry() != nil {
		t.Errorf("the provider was asked to remove %v, want no registry", removed)
	}
}
