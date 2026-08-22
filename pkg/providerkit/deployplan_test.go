package providerkit

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const deploymentID = "0123456789abcdef0123456789abcdef"

func productionRequest(apps ...*contractv1.ManifestApp) *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest:    &contractv1.Manifest{Slug: "shop", Apps: apps},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func TestBuildDeployPlanNamesAnInfraStackAndOneStackPerApp(t *testing.T) {
	t.Parallel()

	plan, err := buildDeployPlan(productionRequest(
		&contractv1.ManifestApp{Name: "web", DeploymentId: deploymentID},
		&contractv1.ManifestApp{Name: "admin", DeploymentId: deploymentID},
	), "p1")
	if err != nil {
		t.Fatalf("buildDeployPlan() error = %v", err)
	}
	if plan.Infra != naming.InfraStack(ProductionEnv) {
		t.Errorf("plan infra stack = %s, want %s", plan.Infra, naming.InfraStack(ProductionEnv))
	}
	if len(plan.Apps) != 2 {
		t.Fatalf("plan carries %d app stacks, want one per app", len(plan.Apps))
	}
	if plan.Pointer != edge.DefaultPointer {
		t.Errorf("a production plan points at %q, want %q", plan.Pointer, edge.DefaultPointer)
	}
	for _, entry := range plan.Apps {
		if plan.Builds[entry.App] != entry.Build.String() {
			t.Errorf("the promotion records %q as %s's build, want %s", plan.Builds[entry.App], entry.App, entry.Build)
		}
		if entry.Stack.Env != ProductionEnv || entry.Stack.App != entry.App {
			t.Errorf("%s's stack is %s, want it named for the app in production", entry.App, entry.Stack)
		}
	}
}

func TestBuildDeployPlanLeavesAnEphemeralPreviewWithoutAnInfraStack(t *testing.T) {
	t.Parallel()

	plan, err := buildDeployPlan(&contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{Slug: "shop", Apps: []*contractv1.ManifestApp{{Name: "web", DeploymentId: deploymentID}}},
		Environment: &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Identity:  "pr-7",
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		},
	}, "p1")
	if err != nil {
		t.Fatalf("buildDeployPlan() error = %v", err)
	}
	if !plan.Infra.IsZero() {
		t.Errorf("an ephemeral preview plans infra stack %s, want none: nothing persists past the pointer", plan.Infra)
	}
	if plan.Pointer != "pr-7" {
		t.Errorf("a preview plan points at %q, want the environment's identity", plan.Pointer)
	}
}

func TestBuildDeployPlanRefusesAnAppNamedForTheInfraStack(t *testing.T) {
	t.Parallel()

	_, err := buildDeployPlan(productionRequest(&contractv1.ManifestApp{Name: naming.InfraApp, DeploymentId: deploymentID}), "p1")
	if err == nil || !strings.Contains(err.Error(), naming.InfraApp) {
		t.Fatalf("buildDeployPlan() with an app named %q = %v, want a refusal naming it", naming.InfraApp, err)
	}
}

func TestBuildAppStackMovesWhenAValueVersionMoves(t *testing.T) {
	t.Parallel()

	before, err := appEntry(&contractv1.ManifestApp{
		Name:         "web",
		DeploymentId: deploymentID,
		Variables:    []*contractv1.ManifestVariable{{Key: "API_URL", Version: 1}},
	}, ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	after, err := appEntry(&contractv1.ManifestApp{
		Name:         "web",
		DeploymentId: deploymentID,
		Variables:    []*contractv1.ManifestVariable{{Key: "API_URL", Version: 2}},
	}, ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stack == after.Stack {
		t.Fatalf("both versions of the same value deploy to %s, so the release does not follow what the app reads", before.Stack)
	}
}

func TestBuildRoundTripsThroughItsRenderedForm(t *testing.T) {
	t.Parallel()

	built, err := NewBuild(deploymentID, ProductionEnv, "v1")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseBuild(built.String())
	if err != nil {
		t.Fatalf("ParseBuild(%q) = %v", built, err)
	}
	if parsed != built {
		t.Errorf("ParseBuild(%q) = %v, want the build it rendered", built, parsed)
	}
	if parsed.Release() != built.Release() {
		t.Errorf("the parsed build releases to %s, want %s", parsed.Release(), built.Release())
	}
}

func TestNewBuildRefusesADeploymentIdNothingCanName(t *testing.T) {
	t.Parallel()

	if _, err := NewBuild("not-a-deployment-id", ProductionEnv, ""); err == nil {
		t.Fatal("NewBuild() with a malformed deployment id succeeded, want a refusal")
	}
	if _, err := NewBuild(deploymentID, "", ""); err == nil {
		t.Fatal("NewBuild() with no environment succeeded, want a refusal: the fingerprint is scoped to one")
	}
}

func TestReclaimTargetsKeepAssetsARemainingReleaseStillServes(t *testing.T) {
	t.Parallel()

	shared, err := NewBuild(deploymentID, ProductionEnv, "shared")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := NewBuild(deploymentID, ProductionEnv, "gone")
	if err != nil {
		t.Fatal(err)
	}

	targets, err := ReclaimTargets("shop", ProductionEnv,
		[]string{"record:web/" + gone.String(), "record:web/" + shared.String()},
		[]string{"record:web/" + shared.String()},
		nil)
	if err != nil {
		t.Fatalf("ReclaimTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("ReclaimTargets() returned %d targets, want one per removed record", len(targets))
	}

	byApp := map[string][]string{}
	for _, target := range targets {
		byApp[target.Build.Fingerprint()] = target.Prefixes
	}
	if len(byApp[gone.Fingerprint()]) == 0 {
		t.Error("the release nothing else serves keeps its stored objects, want them reclaimed")
	}
	for _, prefix := range byApp[shared.Fingerprint()] {
		if !strings.HasSuffix(prefix, "isr/") {
			t.Errorf("a release another pointer still serves would lose %s, want only its cache reclaimed", prefix)
		}
	}
}

func TestReclaimTargetsRefuseARecordKeyNothingWrote(t *testing.T) {
	t.Parallel()

	if _, err := ReclaimTargets("shop", ProductionEnv, []string{"record:web"}, nil, nil); err == nil {
		t.Fatal("ReclaimTargets() accepted a key carrying no identity, want a refusal")
	}
}

func TestClassifyStacksSplitsProductionFromPreview(t *testing.T) {
	t.Parallel()

	release := naming.NewRelease(deploymentID, "f")
	entries := []StackEntry{
		{Name: naming.InfraStack(ProductionEnv)},
		{Name: naming.AppStack(ProductionEnv, "web", release)},
		{Name: naming.InfraStack("staging")},
		{Name: naming.AppStack("pr-7", "web", release)},
	}

	infra, apps, pointers := classifyStacks(entries, ClassProduction)
	if len(infra) != 1 || len(apps) != 1 || len(pointers) != 1 {
		t.Fatalf("production carries %v / %v / %v, want only the production stacks", infra, apps, pointers)
	}

	infra, apps, pointers = classifyStacks(entries, ClassPreview)
	if len(infra) != 1 || len(apps) != 1 {
		t.Fatalf("preview carries %v / %v, want the staging infra and the pr-7 app", infra, apps)
	}
	if len(pointers) != 2 {
		t.Errorf("preview carries pointers %v, want one per preview environment", pointers)
	}
}
