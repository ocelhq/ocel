package providerserver

import (
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

	targets, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
		[]router.DeploymentRecord{{App: "web", Build: gone.String()}, {App: "web", Build: shared.String()}},
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

func TestReclaimTargetsRefuseARecordNamingNoBuild(t *testing.T) {
	t.Parallel()

	for _, build := range []string{"", "garbage", "ocel/web@sha256:" + strings.Repeat("a", 64)} {
		if _, err := ReclaimTargets("shop", stackrecords.ProductionEnv, []router.DeploymentRecord{{App: "web", Build: build}}, nil, nil); err == nil {
			t.Errorf("ReclaimTargets() over a record of build %q returned no refusal, want one: a record whose build names no stack is corrupt", build)
		}
	}
}

func TestReclaimTargetsLeaveAContainerReleaseToTheBoxThatRunsIt(t *testing.T) {
	t.Parallel()

	container, function := reclaimedBuild(t, "container"), reclaimedBuild(t, "function")
	targets, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
		[]router.DeploymentRecord{
			{App: "web", Build: container.String(), Image: "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)},
			{App: "api", Build: function.String()},
		},
		nil, nil)
	if err != nil {
		t.Fatalf("ReclaimTargets() over a record of a container release = %v, want the release the box keeps by image reference left to it: a container app puts nothing in the artifact store and its container comes down with the pointer", err)
	}
	if len(targets) != 1 || targets[0].App != "api" {
		t.Fatalf("ReclaimTargets() returned %+v, want only the function release", targets)
	}
}
