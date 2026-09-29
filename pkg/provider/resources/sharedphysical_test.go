package resources_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type countedStore struct {
	keyvalue.Store
	mu    sync.Mutex
	reads int
	lists int
}

func (c *countedStore) Read(ctx context.Context, key keyvalue.Key) (keyvalue.Entry, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.Store.Read(ctx, key)
}

func (c *countedStore) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.Store.List(ctx, in, under...)
}

func (c *countedStore) counted() (reads, lists int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, c.lists
}

func (c *countedStore) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads, c.lists = 0, 0
}

type serviceEveryReleaseRevises struct {
	revision         string
	removed          []string
	removedRevisions []string
}

func (s *serviceEveryReleaseRevises) hooks() resources.Hooks {
	return resources.Hooks{
		Functions: &resources.FunctionHooks{
			Provision: func(_ context.Context, spec provider.StackSpec, _ progress.Progress) ([]provider.Function, error) {
				var functions []provider.Function
				for _, fn := range spec.App.Functions {
					functions = append(functions, provider.Function{Name: fn.Name, Physical: "shop-prod-web-" + fn.Name, Revision: s.revision})
				}
				return functions, nil
			},
			Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Progress) error {
				for _, function := range functions {
					s.removed = append(s.removed, function.Physical)
				}
				return nil
			},
			RemoveRevisions: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Progress) error {
				for _, function := range functions {
					s.removedRevisions = append(s.removedRevisions, function.Revision)
				}
				return nil
			},
		},
		Containers: &resources.ContainerHooks{
			Provision: func(_ context.Context, spec provider.StackSpec, _ progress.Progress) ([]provider.AppContainer, error) {
				return []provider.AppContainer{{Name: spec.App.App, Physical: "shop-prod-" + spec.App.App, Revision: s.revision}}, nil
			},
			Remove: func(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ progress.Progress) error {
				for _, container := range containers {
					s.removed = append(s.removed, container.Physical)
				}
				return nil
			},
			RemoveRevisions: func(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ progress.Progress) error {
				for _, container := range containers {
					s.removedRevisions = append(s.removedRevisions, container.Revision)
				}
				return nil
			},
		},
	}
}

func releaseRef(deploymentID string) provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
		Name:    naming.AppStack("prod", "web", naming.NewRelease(deploymentID, "f1")),
	}
}

type releases struct {
	t      *testing.T
	store  keyvalue.Store
	stacks provider.Stacks
}

func newReleases(t *testing.T, store keyvalue.Store, hooks resources.Hooks) releases {
	return releases{t: t, store: store, stacks: resources.NewHookStacks(store, fake.NewArtifacts(), hooks)}
}

func (r releases) provision(ref provider.StackRef, app *provider.AppSpec) {
	r.t.Helper()
	ctx := context.Background()
	result, err := r.stacks.Provision(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: app}, nil)
	if err != nil {
		r.t.Fatalf("Provision(%s) = %v", ref.Name, err)
	}
	if err := stackrecords.Write(ctx, r.store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackApp, Functions: result.Functions, Containers: result.Containers,
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r releases) destroy(ref provider.StackRef) {
	r.t.Helper()
	if err := r.stacks.Destroy(context.Background(), ref, nil); err != nil {
		r.t.Fatalf("Destroy(%s) = %v", ref.Name, err)
	}
}

func (r releases) forget(ref provider.StackRef) {
	r.t.Helper()
	if err := stackrecords.Forget(context.Background(), r.store, ref.Tier, ref.Project, ref.Name); err != nil {
		r.t.Fatal(err)
	}
}

func functionApp() *provider.AppSpec {
	return &provider.AppSpec{App: "web", Compute: provider.ComputeServerless, Functions: []provider.FunctionSpec{{Name: "api"}}}
}

func TestDestroyOfOneReleaseTakesOnlyItsRevisionFromAFunctionAnotherReleaseServes(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	dropped, surviving := releaseRef("d1"), releaseRef("d2")
	service.revision = "shop-prod-web-api-00001"
	released.provision(dropped, functionApp())
	service.revision = "shop-prod-web-api-00002"
	released.provision(surviving, functionApp())

	released.destroy(dropped)

	if len(service.removed) != 0 {
		t.Errorf("Destroy() took down %v, which the surviving release still serves from", service.removed)
	}
	if want := []string{"shop-prod-web-api-00001"}; !slices.Equal(service.removedRevisions, want) {
		t.Errorf("Destroy() took the revisions %v, want %v: only the revision the dropped release deployed", service.removedRevisions, want)
	}
}

func TestDestroyOfOneReleaseLeavesARevisionAnotherReleaseAlsoServes(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{revision: "shop-prod-web-api-00001"}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	dropped, surviving := releaseRef("d1"), releaseRef("d2")
	released.provision(dropped, functionApp())
	released.provision(surviving, functionApp())

	released.destroy(dropped)

	if len(service.removed) != 0 || len(service.removedRevisions) != 0 {
		t.Errorf("Destroy() took down %v and the revisions %v, and the surviving release serves that very revision",
			service.removed, service.removedRevisions)
	}
}

func TestDestroyOfTheLastReleaseNamingAFunctionTakesTheFunctionDown(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	dropped, last := releaseRef("d1"), releaseRef("d2")
	service.revision = "shop-prod-web-api-00001"
	released.provision(dropped, functionApp())
	service.revision = "shop-prod-web-api-00002"
	released.provision(last, functionApp())

	released.destroy(dropped)
	released.forget(dropped)
	released.destroy(last)

	if want := []string{"shop-prod-web-api"}; !slices.Equal(service.removed, want) {
		t.Errorf("Destroy() of the last release took down %v, want %v: nothing else names it", service.removed, want)
	}
}

func TestTwoReleasesDestroyedBeforeEitherRecordIsForgottenStillTakeTheirFunctionDown(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{}
	store := fake.NewKeyValues()
	released := newReleases(t, store, service.hooks())
	first, second := releaseRef("d1"), releaseRef("d2")
	service.revision = "shop-prod-web-api-00001"
	released.provision(first, functionApp())
	service.revision = "shop-prod-web-api-00002"
	released.provision(second, functionApp())

	released.destroy(first)
	released.destroy(second)

	if want := []string{"shop-prod-web-api"}; !slices.Equal(service.removed, want) {
		t.Errorf("two destroys whose records were both still written took down %v, want %v: each saw the other's record and left the service to it",
			service.removed, want)
	}
	shared := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootSharedPhysicals, Path: []string{"shop"}}
	if left, err := store.List(context.Background(), shared); err != nil || len(left) != 0 {
		t.Errorf("the destroys left %v, %v behind, want nothing: a teardown leaves no bytes", left, err)
	}
}

func TestDestroyOfOneReleaseTakesOnlyItsRevisionFromAContainerAnotherReleaseServes(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	dropped, surviving := releaseRef("d1"), releaseRef("d2")
	service.revision = "shop-prod-web-00001"
	released.provision(dropped, containerApp("web"))
	service.revision = "shop-prod-web-00002"
	released.provision(surviving, containerApp("web"))

	released.destroy(dropped)

	if len(service.removed) != 0 {
		t.Errorf("Destroy() took down %v, which the surviving release still serves from", service.removed)
	}
	if want := []string{"shop-prod-web-00001"}; !slices.Equal(service.removedRevisions, want) {
		t.Errorf("Destroy() took the revisions %v, want %v", service.removedRevisions, want)
	}
}

func TestADestroyOnAProviderWhoseReleasesShareNothingReadsOnlyItsOwnStack(t *testing.T) {
	t.Parallel()

	store := &countedStore{Store: fake.NewKeyValues()}
	own := &withContainers{buckets: &buckets{}}
	hooks := own.hooks()
	hooks.Containers.RemoveRevisions = nil
	released := newReleases(t, store, hooks)
	for i := range 3 {
		released.provision(releaseRef(fmt.Sprintf("d%d", i)), containerApp("web"))
	}
	store.reset()

	released.destroy(releaseRef("d0"))

	if reads, lists := store.counted(); reads != 1 || lists != 0 {
		t.Errorf("Destroy() read %d entries and listed %d partitions, want the one stack record it destroys and no listing: nothing it removes is shared", reads, lists)
	}
	if len(own.removed) != 1 {
		t.Errorf("Destroy() took down %v, want its own container", own.removed)
	}
}

func TestTearingDownEveryReleaseReadsInProportionToTheReleases(t *testing.T) {
	t.Parallel()

	const readsPerRelease = 2
	for _, count := range []int{2, 8} {
		store := &countedStore{Store: fake.NewKeyValues()}
		service := &serviceEveryReleaseRevises{}
		released := newReleases(t, store, service.hooks())
		refs := make([]provider.StackRef, count)
		for i := range refs {
			refs[i] = releaseRef(fmt.Sprintf("d%d", i))
			service.revision = fmt.Sprintf("shop-prod-web-api-%05d", i)
			released.provision(refs[i], functionApp())
		}
		store.reset()

		for _, ref := range refs {
			released.destroy(ref)
		}

		reads, lists := store.counted()
		if lists != 0 {
			t.Errorf("tearing down %d releases listed %d partitions, want none: a listing per destroy makes a teardown quadratic", count, lists)
		}
		if reads > readsPerRelease*count {
			t.Errorf("tearing down %d releases read %d entries, want at most %d per release", count, reads, readsPerRelease)
		}
		if want := []string{"shop-prod-web-api"}; !slices.Equal(service.removed, want) {
			t.Errorf("tearing down %d releases took down %v, want %v once", count, service.removed, want)
		}
	}
}
