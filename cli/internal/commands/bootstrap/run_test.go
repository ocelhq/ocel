package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
)

func catalogue() []provider.Feature {
	return []provider.Feature{
		{Name: featureISR, Summary: "incremental static regeneration"},
		{Name: featureImageOptimization, Summary: "on-demand image optimization"},
		{Name: provider.FeatureVarsKey, Summary: "a key the variables are sealed under"},
		{Name: featureRelayEdge, Summary: "a relay front", DependsOn: []string{featureISR}, Needs: []string{provider.NeedsEdgePrefix + "relay"}},
		{Name: featureDirectEdge, Summary: "a direct front", Needs: []string{provider.NeedsEdgePrefix + "direct"}},
	}
}

func bootstrapProject(t *testing.T, declaration string, installed ...string) (clitest.FakeProject, cmddeps.Deps) {
	t.Helper()
	project, deps := clitest.SetUpMonorepoProject(t, declaration)
	project.Provider.FakeBootstrap().Offers(catalogue()...)
	clitest.Bootstrap(t, project.Provider, environment.TierProduction, installed...)
	return project, deps
}

func mixedPlan() provider.Plan {
	return provider.Plan{Groups: []provider.ChangeGroup{
		{
			Kind: provider.StackGroupKind, Name: "fake/ocel-production-core", Action: provider.ActionUpdate,
			Changes: []provider.Change{
				{Kind: "Fake::Function", Name: "OcelDispatchFunction", Action: provider.ActionUpdate},
				{Kind: "Fake::Secret", Name: "OcelOriginSecret", Action: provider.ActionReplace, Reason: "rotation forces replacement"},
			},
		},
		{
			Kind: provider.StackGroupKind, Name: "fake/ocel-production-image-optimization", Feature: featureImageOptimization, Action: provider.ActionCreate,
			Changes: []provider.Change{{Kind: "Fake::Function", Name: "OcelImageFunction", Action: provider.ActionCreate}},
		},
		{
			Kind: provider.StackGroupKind, Name: "fake/ocel-production-isr", Feature: featureISR, Action: provider.ActionDelete,
			Reason: "web, api were deployed against it", Slow: true,
			Changes: []provider.Change{{Kind: "Fake::Table", Name: "OcelRevalidationTable", Action: provider.ActionDelete}},
		},
		{Kind: provider.StackGroupKind, Name: "fake/ocel-production-secrets", Feature: "secrets", Action: provider.ActionKeep, Reason: "already current"},
	}}
}

func withRelayEdge(plan provider.Plan) provider.Plan {
	plan.Groups = append(plan.Groups, provider.ChangeGroup{
		Kind: edge.EdgeGroupKind, Name: edge.EdgeGroupName("relay"), Feature: featureRelayEdge, Action: provider.ActionCreate,
		Changes: []provider.Change{
			{Kind: "Fake::EdgeBucket", Name: "ocel-edge-cache", Action: provider.ActionCreate},
			{Kind: "Fake::EdgeScript", Name: "ocel-deployments-store", Action: provider.ActionCreate},
		},
	})
	return plan
}

func keepPlan() provider.Plan {
	return provider.Plan{Groups: []provider.ChangeGroup{
		{Kind: provider.StackGroupKind, Name: "fake/ocel-production-core", Action: provider.ActionKeep, Reason: "already current"},
	}}
}

func applies(t *testing.T, project clitest.FakeProject) []*contractv1.BootstrapRequest {
	t.Helper()
	var applied []*contractv1.BootstrapRequest
	for _, req := range clitest.RequestsTo[*contractv1.BootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceBootstrapProcedure) {
		if !req.GetDry() {
			applied = append(applied, req)
		}
	}
	return applied
}

func intent(req *contractv1.BootstrapRequest) string {
	said := "features=" + strings.Join(req.GetFeatures(), ",")
	if len(req.GetRemove()) > 0 {
		said += " remove=" + strings.Join(req.GetRemove(), ",")
	}
	said += " force=" + boolWord(req.GetForce()) + " acceptReplacements=" + boolWord(req.GetAcceptReplacements())
	if req.RepairOnDeploy != nil {
		said += " repairOnDeploy=" + boolWord(req.GetRepairOnDeploy())
	}
	return said
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func intents(t *testing.T, project clitest.FakeProject) []string {
	t.Helper()
	var said []string
	for _, req := range applies(t, project) {
		said = append(said, intent(req))
	}
	return said
}

func removalsAsked(t *testing.T, project clitest.FakeProject) (planned, removed int) {
	t.Helper()
	planned = len(clitest.RequestsTo[*contractv1.BootstrapScope](t, project.Requests, contractv1connect.ProviderServicePlanRemoveBootstrapProcedure))
	removed = len(clitest.RequestsTo[*contractv1.BootstrapScope](t, project.Requests, contractv1connect.ProviderServiceRemoveBootstrapProcedure))
	return planned, removed
}

func writtenByANewerOcel(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	running := version.Version
	version.Version = "1.4.0"
	t.Cleanup(func() { version.Version = running })
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	clitest.InstallProvider(t, "fake", func(dest string) error { return os.Symlink(testBinary, dest) })
	project.Provider.FakeBootstrap().SetWriter("1.9.0")
}

func TestRunBootstrapDestroy(t *testing.T) {
	t.Run("--yes skips the phrase and the terminal requirement", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if strings.Contains(out, "Type the environment name") {
			t.Errorf("stdout = %q, want --yes to skip the typed phrase", out)
		}
		if !strings.Contains(out, "Removed the production bootstrap") {
			t.Errorf("stdout = %q, want the production teardown", out)
		}
		if !strings.Contains(out, "ocel  dev  test-app › production") {
			t.Errorf("stdout = %q, want the teardown to say which account it runs in", out)
		}
	})

	t.Run("the bypass env skips the phrase and says so", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")
		t.Setenv(consent.BypassEnv, "production")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
		}
		if strings.Contains(stdout.String(), "Type the environment name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed phrase", stdout.String())
		}
		if want := "WARN  [check] " + consent.BypassEnv + "=production: removing the production bootstrap without confirmation"; !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want the run to warn %q so an unconfirmed teardown is never silent", stdout.String(), want)
		}
	})

	t.Run("a bypass naming the other bootstrap is refused, not ignored", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")
		t.Setenv(consent.BypassEnv, "preview")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{}, &stdout, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the mismatched-bypass refusal")
		}
		for _, want := range []string{consent.BypassEnv, "preview", "production"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("--dry prints the plan, removes nothing, and needs no terminal", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Dry: true}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"This will permanently remove the production bootstrap",
			"– fake-production-isr  [isr]",
			"– fake-production  [core]  — the core every feature above was built on (slow)",
			"– relay/edge",
			"    – relay-front  Fake::Edge::Front",
			"Run without --dry to destroy.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want --dry to print %q", out, want)
			}
		}
		if strings.Contains(out, "Removed the production bootstrap") {
			t.Errorf("stdout = %q, want --dry to remove nothing", out)
		}
		if planned, removed := removalsAsked(t, project); planned != 1 || removed != 0 {
			t.Errorf("provider was asked to plan %d and remove %d times, want the plan alone", planned, removed)
		}
	})

	t.Run("nothing installed is a clean no-op, not a teardown", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")
		if err := project.Provider.FakeBootstrap().Remove(context.Background(), environment.TierProduction, nil); err != nil {
			t.Fatal(err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Nothing to destroy: the production environment is not bootstrapped") {
			t.Errorf("stdout = %q, want it to declare the no-op", out)
		}
		for _, unwanted := range []string{"This will permanently remove", "This cannot be undone", "Removed the production bootstrap"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("stdout = %q, want no %q: an empty plan must stop before the warning and the teardown", out, unwanted)
			}
		}
	})

	t.Run("--dry with nothing installed declares the no-op and offers nothing", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")
		if err := project.Provider.FakeBootstrap().Remove(context.Background(), environment.TierProduction, nil); err != nil {
			t.Fatal(err)
		}

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Dry: true}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("RunDestroy err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Nothing to destroy: the production environment is not bootstrapped") {
			t.Errorf("stdout = %q, want it to declare the no-op", out)
		}
		if strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want no offer to destroy what is not there", out)
		}
	})

	t.Run("without a terminal, a phrase it cannot ask for is a refusal", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")

		var stdout bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{}, &stdout, strings.NewReader(""))
		if err == nil {
			t.Fatal("RunDestroy err = nil, want the no-terminal refusal")
		}
		for _, want := range []string{"needs a terminal", "--yes", consent.BypassEnv} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})
}

func TestRemovingABootstrapAsksForItsNameWhileTheRunIsHeldAfterThePlanItShows(t *testing.T) {
	project, deps := bootstrapProject(t, "", featureISR)
	deps.StdinIsTerminal = func(io.Reader) bool { return true }
	deps.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}

	var stream, stdout bytes.Buffer
	clitest.AttachTerminalSink(deps, &stream)
	if err := RunDestroy(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{}, &stdout, strings.NewReader("production\n")); err != nil {
		t.Fatalf("RunDestroy err = %v; stream=%s stdout=%s", err, stream.String(), stdout.String())
	}

	evs := runEvents(t, stream.String())
	shown := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetPlan() != nil })
	if shown < 0 || evs[shown].GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("the removal plan was not shown in the plan phase: %s", stream.String())
	}
	waiting := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil })
	if waiting < shown {
		t.Fatalf("held at event %d, plan shown at %d: want the run held to ask once the plan is shown: %s", waiting, shown, stream.String())
	}
	resumed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if resumed < waiting || evs[resumed].GetResumed().GetReason() != "answered" {
		t.Fatalf("resumed at event %d, held at %d: want the run resumed once answered: %s", resumed, waiting, stream.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() || result.GetHeadline() != "Removed the production bootstrap" {
		t.Errorf("result = %v, want the run to end reporting the removal", result)
	}
	if planned, removed := removalsAsked(t, project); planned != 1 || removed != 1 {
		t.Errorf("provider was asked to plan %d and remove %d times, want the plan and the teardown consented to", planned, removed)
	}
}

func TestRunBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		t.Parallel()

		err := Run(context.Background(), clitest.NewDeps(), t.TempDir(), environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := Run(context.Background(), clitest.NewDeps(), root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
	})
}

func TestBootstrapShowsItsPlan(t *testing.T) {
	t.Run("it renders every group and the tally before applying", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		project.Provider.FakeBootstrap().PlansWith(mixedPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Proposed changes to the production bootstrap",
			"~ fake/ocel-production-core  [core]",
			"    ~ OcelDispatchFunction  Fake::Function",
			"    ± OcelOriginSecret      Fake::Secret   — rotation forces replacement",
			"+ fake/ocel-production-image-optimization  [image-optimization]",
			"– fake/ocel-production-isr  [isr]  — web, api were deployed against it (slow)",
			"    – OcelRevalidationTable  Fake::Table",
			"1 to create, 1 to update, 1 to replace, 1 to delete, 1 unchanged.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "fake/ocel-production-secrets") {
			t.Errorf("a group that keeps everything took a row; got:\n%s", out)
		}
		if got := applies(t, project); len(got) != 1 {
			t.Errorf("provider was asked to apply %d times, want the apply the plan described", len(got))
		}
	})

	t.Run("the selected edge is listed beside the stacks, in its own vocabulary", func(t *testing.T) {
		project, deps := bootstrapProject(t, "  edge: \"relay\",\n", featureISR)
		project.Provider.FakeBootstrap().PlansWith(withRelayEdge(mixedPlan()))

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Proposed changes to the production bootstrap, fronted by the relay edge:",
			"+ relay/edge  [relay-edge]",
			"    + ocel-edge-cache         Fake::EdgeBucket",
			"    + ocel-deployments-store  Fake::EdgeScript",
			"3 to create, 1 to update, 1 to replace, 1 to delete, 1 unchanged.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "edge relay/edge") {
			t.Errorf("the group named its kind on top of its vendor prefix; got:\n%s", out)
		}
		if got := applies(t, project); len(got) == 0 {
			t.Error("provider was asked to apply nothing, want the apply the plan described")
		}
	})

	t.Run("credentials the plan cannot reach stop the run before it prints half a plan", func(t *testing.T) {
		project, deps := bootstrapProject(t, "  edge: \"relay\",\n", featureISR)
		project.Provider.FakeBootstrap().RefusePlan(errors.New("plan the relay edge bootstrap: FAKE_RELAY_ACCOUNT is not set; export it and re-run"))

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Dry: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runBootstrap err = nil, want the missing credential to stop the plan; stdout=%s", stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "FAKE_RELAY_ACCOUNT is not set") {
			t.Errorf("stdout = %q, want the failure to name the variable that is missing", out)
		}
		if strings.Contains(out, "Proposed changes") {
			t.Errorf("a plan missing its edge was printed anyway; got:\n%s", out)
		}
		if strings.Contains(out, "Run without --dry to apply.") {
			t.Errorf("a failed plan offered the apply; got:\n%s", out)
		}
		if got := applies(t, project); len(got) != 0 {
			t.Errorf("a failed plan reached the provider's apply: %v", intents(t, project))
		}
	})

	t.Run("--dry stops at the plan", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		project.Provider.FakeBootstrap().PlansWith(mixedPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Proposed changes to the production bootstrap") {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to apply.") {
			t.Errorf("stdout = %q, want --dry to say how to apply it", out)
		}
		if got := applies(t, project); len(got) != 0 {
			t.Errorf("--dry reached the provider's apply: %v", intents(t, project))
		}
	})

	t.Run("a plan that changes nothing still applies, because the apply is the repair path", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		project.Provider.FakeBootstrap().PlansWith(keepPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Nothing in the production bootstrap's infrastructure changes: applying only refreshes its seals and records") {
			t.Errorf("stdout = %q, want it to say what applying an all-keep plan is still for", out)
		}
		if got := applies(t, project); len(got) != 1 {
			t.Errorf("provider was asked to apply %d times, want the apply that refreshes what no plan group covers", len(got))
		}
	})

	t.Run("a provider that plans nothing still applies", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		project.Provider.FakeBootstrap().PlansWith(provider.Plan{})

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Nothing in the production bootstrap's infrastructure changes: applying only refreshes its seals and records") {
			t.Errorf("stdout = %q, want the confirm never to float over a void", stdout.String())
		}
		if got := applies(t, project); len(got) != 1 {
			t.Errorf("provider was asked to apply %d times, want the apply to go through", len(got))
		}
	})

	t.Run("--remove passes the force the apply needs, and leaves the rest installed", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR, featureImageOptimization)
		project.Provider.FakeBootstrap().PlansWith(mixedPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Remove: featureISR, Force: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– fake/ocel-production-isr") {
			t.Errorf("stdout = %q, want the deletion shown before it is applied", stdout.String())
		}
		if got := intents(t, project); len(got) != 1 || got[0] != "features=image-optimization remove=isr force=true acceptReplacements=true" {
			t.Errorf("provider was asked to apply %v, want the named removal passed through and the unnamed feature ensured", got)
		}
	})

	t.Run("an installed feature no flag names is not the subject of the plan", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR, featureImageOptimization)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Features: featureISR, FeaturesDeclared: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if got := intents(t, project); len(got) != 1 || got[0] != "features=isr force=false acceptReplacements=true" {
			t.Errorf("provider was asked to apply %v, want image-optimization neither ensured nor removed by a run that never named it", got)
		}
	})

	t.Run("--features and --remove naming the same feature is refused before any plan", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Features: featureISR, FeaturesDeclared: true, Remove: featureISR}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want a feature named both ways refused")
		}
		for _, want := range []string{"--features", "--remove", "isr"} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want the refusal to name %q", stdout.String(), want)
			}
		}
		if planned := clitest.RequestsTo[*contractv1.BootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceBootstrapProcedure); len(planned) != 0 {
			t.Errorf("the provider was asked to plan despite the contradiction: %v", planned)
		}
	})

	t.Run("--remove of a feature that is not installed says so and stops", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Remove: featureImageOptimization}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "image-optimization is not in the production bootstrap, so there is nothing to remove.") {
			t.Errorf("stdout = %q, want it to say the named feature was never there", stdout.String())
		}
		if got := applies(t, project); len(got) != 0 {
			t.Errorf("provider was asked to apply %v, want a run that was only a removal of nothing to apply nothing", intents(t, project))
		}
	})

	t.Run("--remove of an absent feature beside --features still ensures what --features named", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Remove: featureImageOptimization, Features: featureISR, FeaturesDeclared: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if got := intents(t, project); len(got) != 1 || got[0] != "features=isr force=false acceptReplacements=true" {
			t.Errorf("provider was asked to apply %v, want the declared features ensured despite the empty removal", got)
		}
	})
}

func TestBootstrapYesMeansYes(t *testing.T) {
	t.Run("an interactive yes on a plan containing a replacement passes the consent it needs", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		project.Provider.FakeBootstrap().PlansWith(mixedPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Features: featureISR, FeaturesDeclared: true}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "± OcelOriginSecret") {
			t.Fatalf("stdout = %q, want the replacement shown before the confirm that covers it", stdout.String())
		}
		if !strings.Contains(stdout.String(), "ocel  dev  test-app › production") {
			t.Errorf("stdout = %q, want the bootstrap to say which account it runs in", stdout.String())
		}
		if got := intents(t, project); len(got) != 1 || !strings.Contains(got[0], "acceptReplacements=true") {
			t.Errorf("provider was asked to apply %v, want a yes on a plan showing ± to reach it as consent to the replacement", got)
		}
	})

	t.Run("one plan and one yes cover an add and a removal together", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR, featureImageOptimization)
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		project.Provider.FakeBootstrap().PlansWith(mixedPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Features: featureImageOptimization, FeaturesDeclared: true, Remove: featureISR}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– fake/ocel-production-isr") {
			t.Fatalf("stdout = %q, want the delete shown before the confirm that covers it", stdout.String())
		}
		if strings.Contains(stdout.String(), "Remove it anyway?") {
			t.Errorf("stdout = %q, want the plan's own delete group to be the question, asked once", stdout.String())
		}
		if got := intents(t, project); len(got) != 1 || got[0] != "features=image-optimization remove=isr force=true acceptReplacements=true" {
			t.Errorf("provider was asked to apply %v, want the add and the removal in the one apply the plan covered", got)
		}
	})

	t.Run("a silent plan still says what the removal takes, and a no stops there", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		deps.StdinIsTerminal = func(io.Reader) bool { return true }
		project.Provider.FakeBootstrap().PlansWith(provider.Plan{})

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Features: noFeatures, FeaturesDeclared: true, Remove: featureISR}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Removing isr from the production bootstrap tears down what it installed.",
			"Not confirmed, so this run changes nothing",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
		if got := applies(t, project); len(got) != 0 {
			t.Errorf("a refused removal reached the provider's apply: %v", intents(t, project))
		}
	})

	t.Run("what the removal takes rides the stream, so a json run consents to something it was shown", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		deps.Presentation = func(io.Writer) terminal.Presentation {
			return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
		}
		project.Provider.FakeBootstrap().PlansWith(provider.Plan{})

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Features: noFeatures, FeaturesDeclared: true, Remove: featureISR}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		want := "Removing isr from the production bootstrap tears down what it installed."
		said := streamMessages(t, stdout.String())
		if !slices.Contains(said, want) {
			t.Errorf("the stream said %v, want it to contain %q — the consent that follows covers it", said, want)
		}
		if strings.Contains(withoutEnvelopes(stdout.String()), want) {
			t.Errorf("the disclosure also went out beside the stream; got:\n%s", stdout.String())
		}
	})
}

func TestTheBootstrapPlanIsAPlanPhaseEventBeforeTheConsentPrompt(t *testing.T) {
	project, deps := bootstrapProject(t, "", featureISR)
	deps.StdinIsTerminal = func(io.Reader) bool { return true }
	deps.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
	}
	project.Provider.FakeBootstrap().PlansWith(mixedPlan())

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stream)
	clitest.AttachTerminalSink(deps, &stdout)
	if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Features: featureISR, FeaturesDeclared: true}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("Run err = %v; stream=%s stdout=%s stderr=%s", err, stream.String(), stdout.String(), stderr.String())
	}

	evs := runEvents(t, stream.String())
	shown := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetPlan() != nil })
	if shown < 0 || evs[shown].GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Fatalf("the bootstrap plan was not shown in the plan phase: %s", stream.String())
	}
	waiting := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil })
	if waiting < shown {
		t.Fatalf("held at event %d, plan shown at %d: want the run held to ask once the plan is shown: %s", waiting, shown, stream.String())
	}
	resumed := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if resumed < waiting || evs[resumed].GetResumed().GetReason() != "answered" {
		t.Fatalf("resumed at event %d, held at %d: want the run resumed once answered: %s", resumed, waiting, stream.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() || result.GetHeadline() != "Bootstrapped the production environment" {
		t.Errorf("result = %v, want the run to end reporting the bootstrap", result)
	}
	if got := applies(t, project); len(got) != 1 {
		t.Errorf("provider was asked to apply %d times, want the one apply consented to", len(got))
	}
}

func runEvents(t *testing.T, out string) []*streamv1.RunEvent {
	t.Helper()
	var evs []*streamv1.RunEvent
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		evs = append(evs, ev)
	}
	return evs
}
func streamMessages(t *testing.T, out string) []string {
	t.Helper()
	var said []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if message, ok := ev["message"].(string); ok && message != "" {
			said = append(said, message)
		}
	}
	return said
}
func withoutEnvelopes(out string) string {
	var loose []string
	for _, line := range strings.Split(out, "\n") {
		if !json.Valid([]byte(line)) {
			loose = append(loose, line)
		}
	}
	return strings.Join(loose, "\n")
}

func TestUnderJSONWhatABootstrapSaysRidesItsRunAndStdoutIsOnlyTheStream(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edge      string
		installed []string
		arrange   func(t *testing.T, project clitest.FakeProject)
		opts      Options
		wants     []string
	}{
		{
			name:      "removing a feature that is not there",
			installed: []string{featureISR},
			opts:      Options{Yes: true, Remove: featureImageOptimization},
			wants:     []string{"image-optimization is not in the production bootstrap, so there is nothing to remove."},
		},
		{
			name:  "features the edge pulls in",
			edge:  "  edge: \"relay\",\n",
			opts:  Options{Yes: true, Dry: true, Features: noFeatures, FeaturesDeclared: true},
			wants: []string{"Also adding feature relay-edge to the production bootstrap: this project's edge needs it", "Also adding feature isr to the production bootstrap: relay-edge needs it"},
		},
		{
			name:      "content going backwards",
			installed: []string{featureISR},
			arrange: func(t *testing.T, project clitest.FakeProject) {
				writtenByANewerOcel(t, project)
				project.Provider.FakeBootstrap().PlansWith(keepPlan())
			},
			opts:  Options{Yes: true, Dry: true},
			wants: []string{"the same shape, older content"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, deps := bootstrapProject(t, tc.edge, tc.installed...)
			deps.Presentation = func(io.Writer) terminal.Presentation {
				return terminal.Resolve(terminal.Conditions{LogFormat: terminal.FormatJSON})
			}
			if tc.arrange != nil {
				tc.arrange(t, project)
			}

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(deps, &stdout)
			if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, tc.opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			if loose := withoutEnvelopes(strings.TrimRight(stdout.String(), "\n")); loose != "" {
				t.Errorf("stdout holds %q beside the stream, want only NDJSON", loose)
			}
			said := strings.Join(streamMessages(t, stdout.String()), "\n")
			for _, want := range tc.wants {
				if !strings.Contains(said, want) {
					t.Errorf("the stream said %q, want it to say %q", said, want)
				}
			}
		})
	}
}

func TestBootstrapDryPreviewsEverything(t *testing.T) {
	t.Run("--dry previews a removal instead of demanding --force", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR, featureImageOptimization)
		project.Provider.FakeBootstrap().PlansWith(mixedPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Dry: true, Remove: featureISR}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "– fake/ocel-production-isr") {
			t.Errorf("stdout = %q, want --dry to show what the removal takes", stdout.String())
		}
		if got := applies(t, project); len(got) != 0 {
			t.Errorf("--dry reached the provider's apply: %v", intents(t, project))
		}
	})

	t.Run("--dry previews a removal a silent provider draws no plan for", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		project.Provider.FakeBootstrap().PlansWith(provider.Plan{})

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Dry: true, Remove: featureISR}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Removing isr from the production bootstrap tears down what it installed.") {
			t.Errorf("stdout = %q, want --dry to say what the removal takes even with no plan to render", stdout.String())
		}
		if got := applies(t, project); len(got) != 0 {
			t.Errorf("--dry reached the provider's apply: %v", intents(t, project))
		}
	})

	t.Run("--dry says the content is going backwards", func(t *testing.T) {
		project, deps := bootstrapProject(t, "", featureISR)
		writtenByANewerOcel(t, project)
		project.Provider.FakeBootstrap().PlansWith(keepPlan())

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "the same shape, older content") {
			t.Errorf("stdout = %q, want --dry to warn about the downgrade it already knows about", stdout.String())
		}
	})
}

func TestBootstrapSendsRepairOnDeploy(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"an unset switch leaves the account as it is", Options{Yes: true}, "features=isr force=false acceptReplacements=true"},
		{"--repair turns it on", Options{Yes: true, RepairDeclared: true, Repair: true}, "features=isr force=false acceptReplacements=true repairOnDeploy=true"},
		{"--repair=false takes it back", Options{Yes: true, RepairDeclared: true}, "features=isr force=false acceptReplacements=true repairOnDeploy=false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, deps := bootstrapProject(t, "", featureISR)

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(deps, &stdout)
			if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, tt.opts, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runBootstrap err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}
			if got := intents(t, project); len(got) != 1 || got[0] != tt.want {
				t.Errorf("provider was asked to apply %v, want %q", got, tt.want)
			}
		})
	}
}

func TestBootstrapSaysWhatItAppliedBeyondWhatWasAsked(t *testing.T) {
	t.Run("a relay project told to apply nothing is told what its edge pulls in", func(t *testing.T) {
		project, deps := bootstrapProject(t, "  edge: \"relay\",\n")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Dry: true, Features: noFeatures, FeaturesDeclared: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		want := "INFO  [plan] Also adding feature relay-edge to the production bootstrap: this project's edge needs it\nINFO  [plan] Also adding feature isr to the production bootstrap: relay-edge needs it\n"
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	})

	t.Run("a bare run on the default edge is told its edge feature too", func(t *testing.T) {
		project, deps := bootstrapProject(t, "")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		want := "Also adding feature relay-edge to the production bootstrap: this project's edge needs it\n"
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	})

	t.Run("a set that names everything applied says nothing", func(t *testing.T) {
		project, deps := bootstrapProject(t, "  edge: \"relay\",\n")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		if err := Run(context.Background(), deps, project.Root, environmentv1.Tier_TIER_PRODUCTION, Options{Yes: true, Dry: true, Features: "isr,relay-edge", FeaturesDeclared: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "Also adding") {
			t.Errorf("stdout = %q, want nothing said where the set named everything applied", stdout.String())
		}
	})
}
