package providerkit

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const deploymentID = "0123456789abcdef0123456789abcdef"

func productionRequest(apps ...*contractv1.ManifestApp) *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest:    &contractv1.Manifest{Slug: "shop", Apps: apps},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func rosterTitles(stages []Stage) []string {
	titles := make([]string, len(stages))
	for i, s := range stages {
		titles[i] = s.Title
	}
	return titles
}

func TestTheDeployRosterIsTheSpineInOrder(t *testing.T) {
	t.Parallel()

	t.Run("environment, shared infrastructure, apps in manifest order, edge, hostnames, promotion", func(t *testing.T) {
		plan, err := buildDeployPlan(productionRequest(
			&contractv1.ManifestApp{Name: "web", DeploymentId: deploymentID},
			&contractv1.ManifestApp{Name: "admin", DeploymentId: deploymentID},
			&contractv1.ManifestApp{Name: "api", DeploymentId: deploymentID},
		), "p1")
		if err != nil {
			t.Fatalf("buildDeployPlan() error = %v", err)
		}
		want := []string{"Environment", "Shared infrastructure", "web", "admin", "api", "Edge", "Hostnames", "Promotion"}
		if got := rosterTitles(newDeployStages(plan).Roster); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("roster = %v, want %v", got, want)
		}
	})

	t.Run("an ephemeral preview has no shared infrastructure to walk through", func(t *testing.T) {
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
		want := []string{"Environment", "web", "Edge", "Promotion"}
		if got := rosterTitles(newDeployStages(plan).Roster); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("roster = %v, want %v", got, want)
		}
	})
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
	if plan.Infra != naming.InfraStack(stackrecords.ProductionEnv) {
		t.Errorf("plan infra stack = %s, want %s", plan.Infra, naming.InfraStack(stackrecords.ProductionEnv))
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
		if entry.Stack.Env != stackrecords.ProductionEnv || entry.Stack.App != entry.App {
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
	}, stackrecords.ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	after, err := appEntry(&contractv1.ManifestApp{
		Name:         "web",
		DeploymentId: deploymentID,
		Variables:    []*contractv1.ManifestVariable{{Key: "API_URL", Version: 2}},
	}, stackrecords.ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	if before.Stack == after.Stack {
		t.Fatalf("both versions of the same value deploy to %s, so the release does not follow what the app reads", before.Stack)
	}
}

func TestReclaimTargetsKeepAssetsARemainingReleaseStillServes(t *testing.T) {
	t.Parallel()

	shared, err := provider.NewBuild(deploymentID, stackrecords.ProductionEnv, "shared")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := provider.NewBuild(deploymentID, stackrecords.ProductionEnv, "gone")
	if err != nil {
		t.Fatal(err)
	}

	targets, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
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

	if _, err := ReclaimTargets("shop", stackrecords.ProductionEnv, []string{"record:web"}, nil, nil); err == nil {
		t.Fatal("ReclaimTargets() accepted a key carrying no identity, want a refusal")
	}
}

func TestClassifyStacksSplitsProductionFromPreview(t *testing.T) {
	t.Parallel()

	release := naming.NewRelease(deploymentID, "f")
	entries := []stackrecords.NamedStack{
		{Name: naming.InfraStack(stackrecords.ProductionEnv)},
		{Name: naming.AppStack(stackrecords.ProductionEnv, "web", release)},
		{Name: naming.InfraStack("staging")},
		{Name: naming.AppStack("pr-7", "web", release)},
	}

	infra, apps, pointers := classifyStacks(entries, edge.ClassProduction)
	if len(infra) != 1 || len(apps) != 1 || len(pointers) != 1 {
		t.Fatalf("production carries %v / %v / %v, want only the production stacks", infra, apps, pointers)
	}

	infra, apps, pointers = classifyStacks(entries, edge.ClassPreview)
	if len(infra) != 1 || len(apps) != 1 {
		t.Fatalf("preview carries %v / %v, want the staging infra and the pr-7 app", infra, apps)
	}
	if len(pointers) != 2 {
		t.Errorf("preview carries pointers %v, want one per preview environment", pointers)
	}
}

func functionRequest(fn *contractv1.ManifestFunction, apps ...*contractv1.ManifestApp) *contractv1.DeployRequest {
	req := productionRequest(apps...)
	req.Manifest.Functions = []*contractv1.ManifestFunction{fn}
	return req
}

func TestBuildDeployPlanRefusesAnAppNameNoHostnameCanCarry(t *testing.T) {
	t.Parallel()

	_, err := buildDeployPlan(productionRequest(
		&contractv1.ManifestApp{Name: "Web", DeploymentId: deploymentID}), "p1")
	if err == nil {
		t.Fatal("buildDeployPlan() accepted an app named \"Web\", so the store would record a name the edge lowercases out of every hostname")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("buildDeployPlan() = %v, want a %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "Web") {
		t.Errorf("buildDeployPlan() = %q, want the refusal to name the app it will not carry", refused.Message)
	}
}

func TestBuildDeployPlanRefusesAFunctionNoDeclaredAppOwns(t *testing.T) {
	t.Parallel()

	web := &contractv1.ManifestApp{Name: "web", DeploymentId: deploymentID}

	t.Run("an undeclared app", func(t *testing.T) {
		_, err := buildDeployPlan(functionRequest(
			&contractv1.ManifestFunction{LogicalName: "admin-server", App: "admin"}, web), "p1")
		if err == nil {
			t.Fatal("buildDeployPlan() accepted a function naming an app no stack stands up, so the deploy would succeed with the route 404ing")
		}
		if !strings.Contains(err.Error(), "admin") {
			t.Errorf("buildDeployPlan() = %v, want the refusal to name the app it cannot find", err)
		}
	})

	t.Run("no app at all", func(t *testing.T) {
		if _, err := buildDeployPlan(functionRequest(
			&contractv1.ManifestFunction{LogicalName: "server"}, web), "p1"); err == nil {
			t.Fatal("buildDeployPlan() accepted a function naming no app, which would ship once into every app that deploys")
		}
	})

	t.Run("a declared app", func(t *testing.T) {
		if _, err := buildDeployPlan(functionRequest(
			&contractv1.ManifestFunction{LogicalName: "server", App: "web"}, web), "p1"); err != nil {
			t.Fatalf("buildDeployPlan() = %v, want a function its own app declares to be accepted", err)
		}
	})
}

const pinnedTestImage = "ocel/api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerRequest(image string) *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{
			Slug: "shop",
			Apps: []*contractv1.ManifestApp{
				{Name: "api", DeploymentId: deploymentID, Compute: string(provider.ComputeContainer)},
				{Name: "web", DeploymentId: deploymentID, Compute: string(provider.ComputeServerless)},
			},
			Containers: []*contractv1.ManifestContainer{
				{App: "api", Image: image, HealthCheckPath: "/"},
			},
		},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func TestAContainerAppIsPromotedUnderTheDigestItWasBuiltAt(t *testing.T) {
	t.Parallel()

	plan, err := buildDeployPlan(containerRequest(pinnedTestImage), "p1")
	if err != nil {
		t.Fatalf("buildDeployPlan() error = %v", err)
	}
	if got := plan.Builds["api"]; got != pinnedTestImage {
		t.Errorf("the promotion records %q as api's build, want %q: rolling back to it must repoint at a retained image rather than rebuild one", got, pinnedTestImage)
	}
}

func TestAServerlessAppBesideAContainerKeepsItsOwnBuildIdentity(t *testing.T) {
	t.Parallel()

	plan, err := buildDeployPlan(containerRequest(pinnedTestImage), "p1")
	if err != nil {
		t.Fatalf("buildDeployPlan() error = %v", err)
	}
	for _, entry := range plan.Apps {
		if entry.App != "web" {
			continue
		}
		if plan.Builds["web"] != entry.Build.String() {
			t.Errorf("the promotion records %q as web's build, want %s", plan.Builds["web"], entry.Build)
		}
	}
}

func TestAContainerNamingAnAppTheManifestDoesNotDeclareRefusesTheDeploy(t *testing.T) {
	t.Parallel()

	req := containerRequest(pinnedTestImage)
	req.Manifest.Containers[0].App = "ghost"

	_, err := buildDeployPlan(req, "p1")
	if err == nil {
		t.Fatal("buildDeployPlan() admitted a container for an app this manifest never declares, and nothing would ever stand it up")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("buildDeployPlan() error = %q, want it to name the app", err)
	}
}

func TestAContainerAppWithNoContainerRefusesTheDeploy(t *testing.T) {
	t.Parallel()

	req := containerRequest(pinnedTestImage)
	req.Manifest.Containers = nil

	_, err := buildDeployPlan(req, "p1")
	if err == nil {
		t.Fatal("buildDeployPlan() admitted an app on container compute with no container, so the promotion would record no image to roll back to")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("buildDeployPlan() error = %q, want it to name the app", err)
	}
}

func TestAContainerAppPackedIntoFunctionsRefusesTheDeploy(t *testing.T) {
	t.Parallel()

	req := containerRequest(pinnedTestImage)
	req.Manifest.Functions = []*contractv1.ManifestFunction{{LogicalName: "api-server", App: "api"}}

	_, err := buildDeployPlan(req, "p1")
	if err == nil {
		t.Fatal("buildDeployPlan() admitted a container app that was packed into functions too, so the process and a zip would both answer the same request with nothing to say which was meant to")
	}
	for _, want := range []string{"api", "api-server"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("buildDeployPlan() error = %q, want it to name %s", err, want)
		}
	}
}

func TestReclaimTargetsLeaveAContainerReleaseToTheBoxThatHoldsIt(t *testing.T) {
	t.Parallel()

	gone, err := provider.NewBuild(deploymentID, stackrecords.ProductionEnv, "gone")
	if err != nil {
		t.Fatal(err)
	}
	targets, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
		[]string{"record:web/ocel/web@sha256:" + strings.Repeat("a", 64), "record:api/" + gone.String()},
		nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets() over a promotion carrying a container release = %v, want the release the box keeps by image reference left to it: a container app puts nothing in the artifact store and its container comes down with the pointer", err)
	}
	if len(targets) != 1 || targets[0].App != "api" {
		t.Fatalf("ReclaimTargets() returned %+v, want only the function release", targets)
	}
}

func TestReclaimTargetsRefuseAKeyThatIsNeitherABuildNorAnImageReference(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"record:web/garbage@",
		"record:web/garbage@sha256:",
		"record:web/@sha256:" + strings.Repeat("a", 64),
		"record:web/garbage@sha256:" + strings.Repeat("z", 64),
		"record:web/garbage@sha512:" + strings.Repeat("a", 128),
	} {
		if _, err := ReclaimTargets("shop", stackrecords.ProductionEnv, []string{key}, nil, nil); err == nil {
			t.Errorf("ReclaimTargets() over %q returned no refusal, and a key that names neither a build nor a digest-pinned image reference is a corrupt key rather than a container release the box reclaims", key)
		}
	}
}
