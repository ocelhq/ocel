package providerserver

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func reclaimedBuild(t *testing.T, values string) provider.Build {
	t.Helper()
	build, err := provider.NewBuild(deploymentID, "p1", stackrecords.ProductionEnv, values)
	if err != nil {
		t.Fatal(err)
	}
	return build
}

func TestReclaimTargetsKeepAssetsARemainingReleaseStillServes(t *testing.T) {
	t.Parallel()

	shared, gone := reclaimedBuild(t, "shared"), reclaimedBuild(t, "gone")

	targets, _, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
		[]router.DeploymentRecord{{App: "web", Build: gone.String()}, {App: "web", Build: shared.String()}},
		[]string{"record:web/" + shared.String()},
		nil, false)
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

func TestReclaimTargetsRefuseARecordNamingNoBuildAndStillTargetTheRest(t *testing.T) {
	t.Parallel()

	kept := reclaimedBuild(t, "kept")
	for _, build := range []string{"", "garbage", "ocel/web@sha256:" + strings.Repeat("a", 64)} {
		targets, refused, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
			[]router.DeploymentRecord{{App: "web", Build: build}, {App: "api", Build: kept.String()}}, nil, nil, false)
		if err == nil {
			t.Errorf("ReclaimTargets() over a record of build %q returned no refusal, want one: a record whose build names no stack is corrupt", build)
		}
		if want := []string{"record:web/" + build}; !slices.Equal(refused, want) {
			t.Errorf("ReclaimTargets() refused %v, want %v", refused, want)
		}
		if len(targets) != 1 || targets[0].App != "api" {
			t.Errorf("ReclaimTargets() over a record of build %q targeted %+v, want the api build beside it still reclaimed", build, targets)
		}
	}
}

func TestReclaimTargetsTargetAContainerReleaseUnlessTheProviderRetainsThem(t *testing.T) {
	t.Parallel()

	container, function := reclaimedBuild(t, "container"), reclaimedBuild(t, "function")
	removed := []router.DeploymentRecord{
		{App: "web", Build: container.String(), Image: "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)},
		{App: "api", Build: function.String()},
	}
	for retained, want := range map[bool][]string{false: {"web", "api"}, true: {"api"}} {
		targets, _, err := ReclaimTargets("shop", stackrecords.ProductionEnv, removed, nil, nil, retained)
		if err != nil {
			t.Fatalf("ReclaimTargets(retained %v) = %v", retained, err)
		}
		var apps []string
		for _, target := range targets {
			apps = append(apps, target.App)
		}
		if !slices.Equal(apps, want) {
			t.Errorf("ReclaimTargets(retained %v) targeted %v, want %v", retained, apps, want)
		}
	}
}
