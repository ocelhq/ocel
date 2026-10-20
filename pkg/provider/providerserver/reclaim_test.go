package providerserver

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func reclaimedRelease(t *testing.T, values string) provider.Release {
	t.Helper()
	build, err := provider.NewRelease(buildID, "p1", stackrecords.ProductionEnv, values)
	if err != nil {
		t.Fatal(err)
	}
	return build
}

func TestReclaimTargetsKeepAssetsARemainingReleaseStillServes(t *testing.T) {
	t.Parallel()

	shared, gone := reclaimedRelease(t, "shared"), reclaimedRelease(t, "gone")

	targets, _, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
		[]router.ReleaseRecord{{App: "web", Release: gone.String()}, {App: "web", Release: shared.String()}},
		[]string{"record:web/" + shared.String()},
		nil, false)
	if err != nil {
		t.Fatalf("ReclaimTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("ReclaimTargets() returned %d targets, want one per removed record", len(targets))
	}

	byApp := map[string][]naming.ObjectKind{}
	for _, target := range targets {
		byApp[target.Release.Fingerprint()] = target.Kinds
	}
	if !slices.Equal(byApp[gone.Fingerprint()], naming.CoordinateObjectKinds) {
		t.Errorf("the release nothing else serves reclaims %v, want every kind of its stored objects", byApp[gone.Fingerprint()])
	}
	if !slices.Equal(byApp[shared.Fingerprint()], naming.ISRObjectKinds) {
		t.Errorf("a release another pointer still serves reclaims %v, want only its cache kinds", byApp[shared.Fingerprint()])
	}
}

func TestReclaimTargetsRefuseARecordNamingNoBuildAndStillTargetTheRest(t *testing.T) {
	t.Parallel()

	kept := reclaimedRelease(t, "kept")
	for _, build := range []string{"", "garbage", "ocel/web@sha256:" + strings.Repeat("a", 64)} {
		targets, refused, err := ReclaimTargets("shop", stackrecords.ProductionEnv,
			[]router.ReleaseRecord{{App: "web", Release: build}, {App: "api", Release: kept.String()}}, nil, nil, false)
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

	container, function := reclaimedRelease(t, "container"), reclaimedRelease(t, "function")
	removed := []router.ReleaseRecord{
		{App: "web", Release: container.String(), Image: "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)},
		{App: "api", Release: function.String()},
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
