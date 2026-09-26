package providerserver

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
)

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
