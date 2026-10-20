package cloudflare

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const defaultNamespace = "ocel"

func TestAnEntryWorkerIsAnOcelNameOfItsTierAndProjectEndingInAHashTag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, "ocel-prod-entry-shop-138c36"},
		{environment.TierPreview, "ocel-preview-entry-shop-a72e31"},
	} {
		got, err := entryWorkerName(defaultNamespace, "shop", tc.tier)
		if err != nil {
			t.Fatalf("entryWorkerName(%s): %v", tc.tier, err)
		}
		if got != tc.want {
			t.Errorf("entryWorkerName(%s) = %q, want %q", tc.tier, got, tc.want)
		}
	}
	if _, err := entryWorkerName(defaultNamespace, "shop", environment.Tier("nonsense")); err == nil {
		t.Error("entryWorkerName(unknown tier) err = nil, want an error")
	}
}

func TestAnEntryWorkerOfALongProjectFitsAWorkerNameAndStaysApartFromItsNeighbour(t *testing.T) {
	t.Parallel()

	slug := strings.Repeat("verylongproject", 5)
	one, err := entryWorkerName(defaultNamespace, slug+"a", environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	two, err := entryWorkerName(defaultNamespace, slug+"b", environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{one, two} {
		if len(name) > maxWorkerNameLength {
			t.Errorf("%q is %d characters, over the %d a worker name allows", name, len(name), maxWorkerNameLength)
		}
		if !strings.HasPrefix(name, "ocel-prod-entry-verylongproject") {
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
		name, err := entryWorkerName(defaultNamespace, "app", tier)
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
	theirs, err := entryWorkerName(other, "shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("entryWorkerName: %v", err)
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
		"ocel-prod-entry-shop-138c36":     true,
		"ocel-preview-entry-shop-a72e31":  true,
		"ocel-prod-entry-shop-foo-d4c1f0": false,
		"ocel-prod-entry-shop":            false,
		"my-worker":                       false,
	} {
		if got, err := ProjectOwnsWorker(defaultNamespace, "shop", script); err != nil || got != want {
			t.Errorf("ProjectOwnsWorker(shop, %q) = %v, %v, want %v", script, got, err, want)
		}
		if got := ownsOf(defaultNamespace, "shop")(script); got != want {
			t.Errorf("projectOwnsScript(shop)(%q) = %v, want %v", script, got, want)
		}
	}
	for _, args := range [][3]string{{"", "shop", "ocel-prod-entry-shop-138c36"}, {defaultNamespace, "", "ocel-prod-entry-shop-138c36"}, {defaultNamespace, "shop", ""}} {
		if owns, err := ProjectOwnsWorker(args[0], args[1], args[2]); err != nil || owns {
			t.Errorf("ProjectOwnsWorker(%q, %q, %q) = true, want false for an empty argument", args[0], args[1], args[2])
		}
	}
}
