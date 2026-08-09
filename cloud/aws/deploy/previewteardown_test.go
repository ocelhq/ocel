package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestPreviewInfraStackFor_PersistentGetsAStackEphemeralGetsNone(t *testing.T) {
	persistent := PreviewInfraStackFor("shop", "staging", true)
	if want := PreviewInfraStackName("shop", "staging"); persistent != want {
		t.Errorf("persistent infra stack = %q, want %q", persistent, want)
	}
	if got := PreviewInfraStackFor("shop", "pr-1", false); got != "" {
		t.Errorf("ephemeral infra stack = %q, want empty (ephemeral previews own no infra stack)", got)
	}
}

func TestPreviewReclaimTargets_UsePointerScopedStackNames(t *testing.T) {
	targets, err := PreviewReclaimTargets("shop", "pr-1", "preview-pr-1", []string{"record:web/b1", "record:api/b2"}, nil, nil)
	if err != nil {
		t.Fatalf("PreviewReclaimTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	byApp := map[string]PruneTarget{}
	for _, tg := range targets {
		byApp[tg.App] = tg
	}
	if got, want := byApp["web"].Stack, PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b1")); got != want {
		t.Errorf("web stack = %q, want the pointer-scoped preview stack %q", got, want)
	}
	// The production reclaim of the same record must resolve a different stack —
	// proving preview and production never collide on stack names.
	prod, _ := ReclaimTargets("shop", "prod", []string{"record:web/b1"}, nil, nil)
	if prod[0].Stack == byApp["web"].Stack {
		t.Error("preview and production reclaim resolved the same stack name")
	}
}

func TestReclaimTargetsFor_BranchesOnPointer(t *testing.T) {
	prod, err := reclaimTargetsFor("shop", "", "prod", []string{"record:web/b1"}, nil, nil)
	if err != nil {
		t.Fatalf("reclaimTargetsFor(production): %v", err)
	}
	if prod[0].Stack != AppDeployStackName("shop", "web", buildOnly("b1")) {
		t.Errorf("empty pointer must resolve production stacks, got %q", prod[0].Stack)
	}
	preview, err := reclaimTargetsFor("shop", "pr-1", "preview-pr-1", []string{"record:web/b1"}, nil, nil)
	if err != nil {
		t.Fatalf("reclaimTargetsFor(preview): %v", err)
	}
	if preview[0].Stack != PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b1")) {
		t.Errorf("named pointer must resolve preview stacks, got %q", preview[0].Stack)
	}
}

func TestClassifyPreviewStacks_SplitsInfraAppAndPointers(t *testing.T) {
	got := classifyPreviewStacks("shop", []string{
		PreviewInfraStackName("shop", "staging"),
		PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
		PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
		PreviewAppDeployStackName("shop", "pr-1", "api", buildOnly("b3")),
		InfraStackName("shop"),                             // production infra — not a preview stack
		AppDeployStackName("shop", "web", buildOnly("b9")), // production app — not a preview stack
		"other--preview-x--web--b1",                        // another project's preview
	})

	sort.Strings(got.InfraStacks)
	sort.Strings(got.AppStacks)
	wantInfra := []string{PreviewInfraStackName("shop", "staging")}
	wantApps := []string{
		PreviewAppDeployStackName("shop", "pr-1", "api", buildOnly("b3")),
		PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
		PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
	}
	sort.Strings(wantApps)
	if !reflect.DeepEqual(got.InfraStacks, wantInfra) {
		t.Errorf("InfraStacks = %v, want %v", got.InfraStacks, wantInfra)
	}
	if !reflect.DeepEqual(got.AppStacks, wantApps) {
		t.Errorf("AppStacks = %v, want %v", got.AppStacks, wantApps)
	}
	if !reflect.DeepEqual(got.Pointers, []string{"pr-1", "staging"}) {
		t.Errorf("Pointers = %v, want [pr-1 staging] (distinct, sorted)", got.Pointers)
	}
}

// Pointers share one generic worker per app, so retiring one while siblings are
// live must leave the script, their routes and the store instance serving
// (ocelhq-5w3).
func TestRemovePreview_DropsOnlyThisPointersRoutesWhileSiblingsRemain(t *testing.T) {
	fake := &recordingRootStack{
		pointerRemoval: edge.PointerRemoval{
			RemainingPointers: 1,
			RemovedRoutes: []edge.RemovedRoute{
				{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"},
				{App: "api", Hostname: "pr-1-bbbbbbbbbb.preview.acme.com"},
			},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if _, err := RemovePreview(ctx, fake, state, Config{}, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}

	want := []routeRemoval{
		{worker: previewGenericName("shop", "web"), hostname: "pr-1-aaaaaaaaaa.preview.acme.com"},
		{worker: previewGenericName("shop", "api"), hostname: "pr-1-bbbbbbbbbb.preview.acme.com"},
	}
	if !reflect.DeepEqual(fake.removedRoutes, want) {
		t.Errorf("removed routes = %+v, want %+v", fake.removedRoutes, want)
	}
	if fake.destroyed != 0 || len(fake.destroyedWorkers) != 0 {
		t.Errorf("destroyed workers %v: the shared generic worker still fronts live pointers", fake.destroyedWorkers)
	}
	if fake.destroyedInstance != 0 {
		t.Error("the store instance still holds the sibling pointers")
	}
}

// The last preview leaves the whole edge footprint garbage, and nothing else
// would ever reclaim it.
func TestRemovePreview_SweepsTheEdgeFootprintWhenItWasTheLastPointer(t *testing.T) {
	fake := &recordingRootStack{
		pointerRemoval: edge.PointerRemoval{
			RemainingPointers: 0,
			RemovedRoutes:     []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}},
		},
		deployedWorkers: []string{
			previewGenericName("shop", "web"),
			workerScriptName("shop-production", "web"),
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if _, err := RemovePreview(ctx, fake, state, Config{}, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}

	if !reflect.DeepEqual(fake.listedPrefixes, []string{previewWorkerPrefix("shop")}) {
		t.Errorf("listed prefixes = %v, want [%s]", fake.listedPrefixes, previewWorkerPrefix("shop"))
	}
	if want := []string{previewGenericName("shop", "web")}; !reflect.DeepEqual(fake.destroyedWorkers, want) {
		t.Errorf("destroyed workers = %v, want %v (production must be untouched)", fake.destroyedWorkers, want)
	}
	if fake.destroyedInstance != 1 {
		t.Errorf("destroyed instances = %d, want 1: the store instance leaks otherwise", fake.destroyedInstance)
	}
	if len(fake.removedRoutes) != 0 {
		t.Errorf("removed routes = %+v, want none: destroying the script takes its routes with it", fake.removedRoutes)
	}
}

// previewWorkerPrefix is un-clamped while the deployed name is clamped to the
// platform's name limit, so a long slug's worker sits outside the prefix the
// sweep lists. Naming it from the removal is the only way it is ever reclaimed.
func TestPreviewSweepWorkers_CoversAClampedNameThePrefixCannotReach(t *testing.T) {
	slug := strings.Repeat("s", 50)
	worker := previewGenericName(slug, "web")
	if strings.HasPrefix(worker, previewWorkerPrefix(slug)) {
		t.Fatalf("previewGenericName(%q, web) = %q is still under prefix %q — pick a slug long enough to clamp", slug, worker, previewWorkerPrefix(slug))
	}

	fake := &recordingRootStack{
		pointerRemoval: edge.PointerRemoval{
			RemovedRoutes: []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}},
		},
		deployedWorkers: []string{worker},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: slug}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if _, err := RemovePreview(ctx, fake, state, Config{}, slug, "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}
	if !slices.Contains(fake.destroyedWorkers, worker) {
		t.Errorf("destroyed workers = %v, want the clamped %q", fake.destroyedWorkers, worker)
	}
}

func TestPreviewSweepWorkers_UnionsTheListedPrefixTheRoutesAndTheRecords(t *testing.T) {
	removal := edge.PointerRemoval{
		PruneResult:   edge.PruneResult{RemovedRecordKeys: []string{"record:api/b1", "record:web/b2"}},
		RemovedRoutes: []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}},
	}
	got := previewSweepWorkers("shop", removal, []string{previewGenericName("shop", "docs")})

	want := []string{
		previewGenericName("shop", "docs"),
		previewGenericName("shop", "web"),
		previewGenericName("shop", "api"),
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sweep workers = %v, want %v (deduplicated union)", got, want)
	}
}

// The store instance is what a re-run needs to name the workers again: destroy it
// while a worker destroy is failing and RemovePointer can never report a removal
// again, so the surviving workers are stranded with no path back to `preview rm`.
func TestRemovePreview_KeepsTheStoreInstanceWhenTheWorkerDestroyFails(t *testing.T) {
	boom := errors.New("cloudflare said no")
	fake := &recordingRootStack{
		pointerRemoval:      edge.PointerRemoval{RemovedRoutes: []edge.RemovedRoute{{App: "web", Hostname: "pr-1-aaaaaaaaaa.preview.acme.com"}}},
		deployedWorkers:     []string{previewGenericName("shop", "web")},
		destroyRootStackErr: boom,
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	_, err = RemovePreview(ctx, fake, state, Config{}, "shop", "pr-1", false, nil, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("RemovePreview error = %v, want it to report %v", err, boom)
	}
	if fake.destroyedInstance != 0 {
		t.Errorf("destroyed instances = %d, want 0: wiping the store strands the workers that survived", fake.destroyedInstance)
	}
}

// A project holding no root-stack state has no store to ask, so the edge itself
// is the only source left for the workers a deploy that died inside reconcile
// left deployed — and nothing else in the system will ever name them again.
func TestRemovePreview_NoRootStackStateSweepsTheEdgeByPrefix(t *testing.T) {
	orphan := previewGenericName("shop", "web")
	fake := &recordingRootStack{deployedWorkers: []string{orphan, workerScriptName("shop-production", "web")}}

	result, err := RemovePreview(context.Background(), fake, nil, Config{}, "shop", "pr-1", false, nil, nil)
	if err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}
	if !reflect.DeepEqual(fake.listedPrefixes, []string{previewWorkerPrefix("shop")}) {
		t.Errorf("listed prefixes = %v, want [%s]", fake.listedPrefixes, previewWorkerPrefix("shop"))
	}
	if want := []string{orphan}; !reflect.DeepEqual(fake.destroyedWorkers, want) {
		t.Errorf("destroyed workers = %v, want %v (production must be untouched)", fake.destroyedWorkers, want)
	}
	// Nothing store-side may be attempted: there is no state to authenticate with.
	if len(fake.removedPointers) != 0 || len(fake.removedRoutes) != 0 || fake.destroyedInstance != 0 {
		t.Errorf("RemovePreview touched the store with no root-stack state: %+v", fake)
	}
	if result.EdgeTornDown {
		t.Error("EdgeTornDown = true with no state: there is no state for the host to forget")
	}
}

// The same path must stay quiet for the common case it also covers: a preview
// that never got as far as deploying anything.
func TestRemovePreview_NoRootStackStateAndNothingDeployedDestroysNothing(t *testing.T) {
	fake := &recordingRootStack{}

	if _, err := RemovePreview(context.Background(), fake, nil, Config{}, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}
	if fake.destroyed != 0 || len(fake.destroyedWorkers) != 0 {
		t.Errorf("destroyed %v with nothing deployed under the prefix", fake.destroyedWorkers)
	}
}

// A store that refuses the removal leaves the shared worker's fate unknown — a
// sibling pointer may still be fronted by it — so the teardown can neither sweep
// it nor report success over it.
func TestRemovePreview_FailedPointerRemovalReportsWhatItLeftStanding(t *testing.T) {
	fake := &recordingRootStack{deployedWorkers: []string{previewGenericName("shop", "web")}}
	// A state the fake cannot authenticate makes its RemovePointer reject.
	stale := edge.RootStackState{edge.RootStackKeySlug: "shop", edge.RootStackKeySecret: "stale"}

	result, err := RemovePreview(context.Background(), fake, stale, Config{}, "shop", "pr-1", false, nil, nil)
	if err == nil {
		t.Fatal("RemovePreview err = nil, want a failure naming what is still live")
	}
	if !strings.Contains(err.Error(), "still") && !strings.Contains(err.Error(), "standing") {
		t.Errorf("error %q does not name what was left live", err)
	}
	if fake.destroyed != 0 || fake.destroyedInstance != 0 {
		t.Errorf("swept the edge on an unknown removal: %+v", fake)
	}
	if result.EdgeTornDown {
		t.Error("EdgeTornDown = true after a failed pointer removal")
	}
}

// EdgeTornDown is the host's licence to forget the persisted state, so it may
// only be true once the workers and the instance are actually gone.
func TestRemovePreview_ReportsTheEdgeTornDownOnlyOnACleanLastPointerSweep(t *testing.T) {
	ctx := context.Background()
	newFake := func(destroyErr error) (*recordingRootStack, edge.RootStackState) {
		f := &recordingRootStack{
			pointerRemoval:      edge.PointerRemoval{RemainingPointers: 0},
			deployedWorkers:     []string{previewGenericName("shop", "web")},
			destroyRootStackErr: destroyErr,
		}
		state, err := f.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}
		return f, state
	}

	clean, state := newFake(nil)
	result, err := RemovePreview(ctx, clean, state, Config{}, "shop", "pr-1", false, nil, nil)
	if err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}
	if !result.EdgeTornDown {
		t.Error("EdgeTornDown = false after a clean last-pointer sweep: the state is kept pointing at a wiped instance")
	}

	broken, state := newFake(errors.New("cloudflare said no"))
	result, err = RemovePreview(ctx, broken, state, Config{}, "shop", "pr-1", false, nil, nil)
	if err == nil {
		t.Fatal("RemovePreview err = nil after a failed worker destroy")
	}
	if result.EdgeTornDown {
		t.Error("EdgeTornDown = true with a worker still standing: the state a re-run needs would be forgotten")
	}

	// A surviving sibling is not a torn-down edge either.
	sibling, state := newFake(nil)
	sibling.pointerRemoval = edge.PointerRemoval{RemainingPointers: 1}
	result, err = RemovePreview(ctx, sibling, state, Config{}, "shop", "pr-1", false, nil, nil)
	if err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}
	if result.EdgeTornDown {
		t.Error("EdgeTornDown = true while a sibling preview still uses the state")
	}
}

func TestClassifyPreviewStacks_ExcludesProductionAndSiblings(t *testing.T) {
	got := classifyPreviewStacks("shop", []string{
		InfraStackName("shop"),
		AppDeployStackName("shop", "web", buildOnly("b1")),
		"shopfoo--preview-x--web--b1", // sibling project whose id has ours as a prefix
	})
	if len(got.InfraStacks) != 0 || len(got.AppStacks) != 0 || len(got.Pointers) != 0 {
		t.Errorf("classifyPreviewStacks matched non-preview / sibling stacks: %+v", got)
	}
}
