package cloudflare

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const defaultNamespace = "ocel"

func TestAnEntryWorkerIsAnOcelNameOfItsTierOriginAndProjectEndingInAHashTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, "ocel-prod-4c73ed-entry-shop-023da1"},
		{environment.TierPreview, "ocel-preview-4c73ed-entry-shop-fff38e"},
	} {
		got, err := testNames.entryWorker("shop", tc.tier)
		if err != nil {
			t.Fatalf("entryWorker(%s): %v", tc.tier, err)
		}
		if got != tc.want {
			t.Errorf("entryWorker(%s) = %q, want %q", tc.tier, got, tc.want)
		}
	}
	if _, err := testNames.entryWorker("shop", environment.Tier("nonsense")); err == nil {
		t.Error("entryWorker(unknown tier) err = nil, want an error")
	}
}

func TestAnEntryWorkerOfALongProjectFitsAWorkerNameAndStaysApartFromItsNeighbour(t *testing.T) {
	t.Parallel()

	slug := strings.Repeat("verylongproject", 5)
	one, err := testNames.entryWorker(slug+"a", environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	two, err := testNames.entryWorker(slug+"b", environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{one, two} {
		if len(name) > maxWorkerNameLength {
			t.Errorf("%q is %d characters, over the %d a worker name allows", name, len(name), maxWorkerNameLength)
		}
		if !strings.HasPrefix(name, "ocel-prod-4c73ed-entry-verylongproj") {
			t.Errorf("%q no longer reads as the project's entry worker", name)
		}
	}
	if one == two {
		t.Fatalf("two projects share the entry worker %q", one)
	}
}

func TestAnEntryWorkerNeverSitsInsideTheAppFence(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		name, err := testNames.entryWorker("app", tier)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(name, naming.AppFence(defaultNamespace)) {
			t.Errorf("%q sits inside the fence %q only app resources are named under", name, naming.AppFence(defaultNamespace))
		}
	}
}

func TestProjectOwnsScriptReadsOnlyThisNamespacesWorkers(t *testing.T) {
	t.Parallel()

	const other = "j-1874-deploy-next-cloudflare"
	theirs, err := originNames{namespace: other, origin: testOrigin}.entryWorker("shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("entryWorker: %v", err)
	}
	if ownsOf(defaultNamespace, "shop")(theirs) {
		t.Errorf("%q reads as this namespace's, so a prune here would take a route off the worker %s deploys", theirs, other)
	}
	if !ownsOf(other, "shop")(theirs) {
		t.Errorf("%q does not read as %s's own worker", theirs, other)
	}
}

func TestAProjectOwnsTheEntryWorkersOfBothItsTiersAndNoOtherProjects(t *testing.T) {
	t.Parallel()

	for script, want := range map[string]bool{
		"ocel-prod-4c73ed-entry-shop-023da1":     true,
		"ocel-preview-4c73ed-entry-shop-fff38e":  true,
		"ocel-prod-4c73ed-entry-shop-foo-243ec9": false,
		"ocel-prod-4c73ed-entry-shop":            false,
		"my-worker":                              false,
	} {
		if got := ownsOf(defaultNamespace, "shop")(script); got != want {
			t.Errorf("ownsScript(shop)(%q) = %v, want %v", script, got, want)
		}
	}
	for _, args := range [][2]string{{"", "ocel-prod-4c73ed-entry-shop-023da1"}, {"shop", ""}} {
		if ownsOf(defaultNamespace, args[0])(args[1]) {
			t.Errorf("ownsScript(%q)(%q) = true, want false for an empty argument", args[0], args[1])
		}
	}
}

func TestAProjectOwnsNoEntryWorkerAnotherOriginDeployedForIt(t *testing.T) {
	t.Parallel()

	for _, tier := range workerTiers {
		theirs, err := otherNames.entryWorker("shop", tier)
		if err != nil {
			t.Fatal(err)
		}
		if ownsOf(defaultNamespace, "shop")(theirs) {
			t.Errorf("%q reads as %s's, so a deploy there would take the route %s serves shop on", theirs, testOrigin, otherOrigin)
		}
	}
}
