package providerserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const buildID = "0123456789abcdef0123456789abcdef"

func productionRequest(apps ...*contractv1.ManifestApp) *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest:    &contractv1.Manifest{Slug: "shop", Apps: apps},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func TestBuildDeploySpecNamesAnInfraStackAndOneStackPerApp(t *testing.T) {
	t.Parallel()

	spec, err := buildDeploySpec(productionRequest(
		&contractv1.ManifestApp{Name: "web", BuildId: buildID},
		&contractv1.ManifestApp{Name: "admin", BuildId: buildID},
	), "p1")
	if err != nil {
		t.Fatalf("buildDeploySpec() error = %v", err)
	}
	if spec.Infra != naming.InfraStack(stackrecords.ProductionEnv) {
		t.Errorf("spec infra stack = %s, want %s", spec.Infra, naming.InfraStack(stackrecords.ProductionEnv))
	}
	if len(spec.Apps) != 2 {
		t.Fatalf("spec has %d app stacks, want one per app", len(spec.Apps))
	}
	if spec.Pointer != router.DefaultPointer {
		t.Errorf("a production spec points at %q, want %q", spec.Pointer, router.DefaultPointer)
	}
	for _, entry := range spec.Apps {
		if spec.Releases[entry.App] != entry.Release.String() {
			t.Errorf("the promotion records %q as %s's release, want %s", spec.Releases[entry.App], entry.App, entry.Release)
		}
		if entry.Stack.Env != stackrecords.ProductionEnv || entry.Stack.App != entry.App {
			t.Errorf("%s's stack is %s, want it named for the app in production", entry.App, entry.Stack)
		}
	}
}

func TestBuildDeploySpecLeavesAnEphemeralPreviewWithoutAnInfraStack(t *testing.T) {
	t.Parallel()

	spec, err := buildDeploySpec(&contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{Slug: "shop", Apps: []*contractv1.ManifestApp{{Name: "web", BuildId: buildID}}},
		Environment: &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Identity:  "pr-7",
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
		},
	}, "p1")
	if err != nil {
		t.Fatalf("buildDeploySpec() error = %v", err)
	}
	if !spec.Infra.IsZero() {
		t.Errorf("an ephemeral preview specifies infra stack %s, want none: nothing persists past the pointer", spec.Infra)
	}
	if spec.Pointer != "pr-7" {
		t.Errorf("a preview spec points at %q, want the environment's identity", spec.Pointer)
	}
}

func TestBuildDeploySpecRefusesAnAppNamedForTheInfraStack(t *testing.T) {
	t.Parallel()

	_, err := buildDeploySpec(productionRequest(&contractv1.ManifestApp{Name: naming.InfraApp, BuildId: buildID}), "p1")
	if err == nil || !strings.Contains(err.Error(), naming.InfraApp) {
		t.Fatalf("buildDeploySpec() with an app named %q = %v, want a refusal naming it", naming.InfraApp, err)
	}
}

func TestBuildAppStackMovesWhenAValueVersionMoves(t *testing.T) {
	t.Parallel()

	before, err := appEntry(&contractv1.ManifestApp{
		Name:      "web",
		BuildId:   buildID,
		Variables: []*contractv1.ManifestVariable{{Key: "API_URL", Version: 1}},
	}, stackrecords.ProductionEnv, "p1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := appEntry(&contractv1.ManifestApp{
		Name:      "web",
		BuildId:   buildID,
		Variables: []*contractv1.ManifestVariable{{Key: "API_URL", Version: 2}},
	}, stackrecords.ProductionEnv, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if before.Stack == after.Stack {
		t.Fatalf("both versions of the same value deploy to %s, so the release does not follow what the app reads", before.Stack)
	}
}

func TestClassifyStacksSplitsProductionFromPreview(t *testing.T) {
	t.Parallel()

	release := naming.NewReleaseToken(buildID, "f")
	entries := []stackrecords.NamedStack{
		{Name: naming.InfraStack(stackrecords.ProductionEnv)},
		{Name: naming.AppStack(stackrecords.ProductionEnv, "web", release)},
		{Name: naming.InfraStack("staging")},
		{Name: naming.AppStack("pr-7", "web", release)},
	}

	infra, apps, pointers := classifyStacks(entries, environment.TierProduction)
	if len(infra) != 1 || len(apps) != 1 || len(pointers) != 1 {
		t.Fatalf("production has %v / %v / %v, want only the production stacks", infra, apps, pointers)
	}

	infra, apps, pointers = classifyStacks(entries, environment.TierPreview)
	if len(infra) != 1 || len(apps) != 1 {
		t.Fatalf("preview has %v / %v, want the staging infra and the pr-7 app", infra, apps)
	}
	if len(pointers) != 2 {
		t.Errorf("preview has pointers %v, want one per preview environment", pointers)
	}
}

func TestBuildDeploySpecRefusesAnAppNameNoHostnameCanContain(t *testing.T) {
	t.Parallel()

	_, err := buildDeploySpec(productionRequest(
		&contractv1.ManifestApp{Name: "Web", BuildId: buildID}), "p1")
	if err == nil {
		t.Fatal("buildDeploySpec() accepted an app named \"Web\", so the store would record a name the edge lowercases out of every hostname")
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("buildDeploySpec() = %v, want a %s refusal", err, refusal.CodeInvalid)
	}
	if !strings.Contains(refused.Message, "Web") {
		t.Errorf("buildDeploySpec() = %q, want the refusal to name the app it will not deploy", refused.Message)
	}
}

const pinnedTestImage = "ocel/api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerRequest(image string) *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{
			Slug: "shop",
			Apps: []*contractv1.ManifestApp{
				{Name: "api", BuildId: buildID, Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{Image: image, HealthCheckPath: "/", MinInstances: 1, MaxInstances: 1}}},
				{Name: "web", BuildId: buildID, Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}}},
			},
		},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func TestEveryAppIsPromotedUnderTheBuildItsOwnDeployProvisioned(t *testing.T) {
	t.Parallel()

	spec, err := buildDeploySpec(containerRequest(pinnedTestImage), "p1")
	if err != nil {
		t.Fatalf("buildDeploySpec() error = %v", err)
	}
	for _, entry := range spec.Apps {
		if spec.Releases[entry.App] != entry.Release.String() {
			t.Errorf("the promotion records %q as %s's release, want %s, the release whose stack this deploy provisions", spec.Releases[entry.App], entry.App, entry.Release)
		}
	}
}

func TestTwoDeploysOfOneBuiltOutputNeverShareABuild(t *testing.T) {
	t.Parallel()

	first, err := buildDeploySpec(containerRequest(pinnedTestImage), "p1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildDeploySpec(containerRequest(pinnedTestImage), "p2")
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"api", "web"} {
		if first.Releases[app] == second.Releases[app] {
			t.Errorf("both deploys promote %s release %s, so the second's record would stand in for the first's when a rollback names it", app, first.Releases[app])
		}
	}
}

func TestAnAppsTagsNameItsBuildAndItsReleaseAndNoDeployment(t *testing.T) {
	t.Parallel()

	spec, err := buildDeploySpec(productionRequest(&contractv1.ManifestApp{Name: "web", BuildId: buildID}), "p1")
	if err != nil {
		t.Fatalf("buildDeploySpec() error = %v", err)
	}
	entry := spec.Apps[0]

	tags := appTags(spec, entry)
	if tags["ocel:build"] != buildID {
		t.Errorf("ocel:build = %q, want the build id %q the app was built under", tags["ocel:build"], buildID)
	}
	if tags["ocel:release"] != entry.Release.Token().String() {
		t.Errorf("ocel:release = %q, want the release token %q", tags["ocel:release"], entry.Release.Token())
	}
	if _, ok := tags["ocel:deployment"]; ok {
		t.Errorf("tags = %v, want no ocel:deployment: the build id is what the stack was built from", tags)
	}
}
