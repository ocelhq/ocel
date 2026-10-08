package resources_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

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
	mu               sync.Mutex
	revision         string
	latest           string
	failProvision    error
	onProvision      func()
	onRemove         func()
	provisioned      []string
	removed          []string
	removedRevisions []string
	functionRemovals [][]string
}

func (s *serviceEveryReleaseRevises) provision(spec provider.StackSpec) (string, error) {
	s.mu.Lock()
	hook := s.onProvision
	s.onProvision = nil
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provisioned = append(s.provisioned, spec.Ref.Name.String())
	s.latest = s.revision
	return s.revision, s.failProvision
}

func (s *serviceEveryReleaseRevises) remove(physical string) {
	if s.onRemove != nil {
		s.onRemove()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, physical)
}

func (s *serviceEveryReleaseRevises) removeRevision(revision string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision == s.latest {
		return false
	}
	s.removedRevisions = append(s.removedRevisions, revision)
	return true
}

func (s *serviceEveryReleaseRevises) removals() (removed, revisions []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.removed), slices.Clone(s.removedRevisions)
}

func (s *serviceEveryReleaseRevises) nameFunctions(_ context.Context, spec provider.StackSpec) ([]provider.Function, error) {
	var functions []provider.Function
	for _, fn := range spec.App.Functions {
		functions = append(functions, provider.Function{Name: fn.Name, Physical: "shop-prod-web-" + fn.Name})
	}
	return functions, nil
}

func (s *serviceEveryReleaseRevises) nameContainers(_ context.Context, spec provider.StackSpec) ([]provider.AppContainer, error) {
	return []provider.AppContainer{{Name: spec.App.App, Physical: "shop-prod-" + spec.App.App}}, nil
}

func (s *serviceEveryReleaseRevises) hooks() resources.Hooks {
	return resources.Hooks{
		Functions: &resources.FunctionHooks{
			Provision: func(ctx context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.Function, error) {
				revision, err := s.provision(spec)
				if err != nil {
					return nil, err
				}
				functions, _ := s.nameFunctions(ctx, spec)
				for i := range functions {
					functions[i].Revision = revision
				}
				return functions, nil
			},
			Remove: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Log) error {
				var call []string
				for _, function := range functions {
					s.remove(function.Physical)
					call = append(call, function.Physical)
				}
				s.mu.Lock()
				s.functionRemovals = append(s.functionRemovals, call)
				s.mu.Unlock()
				return nil
			},
			Shared: &resources.SharedHooks[provider.Function]{
				Name: s.nameFunctions,
				RemoveRevisions: func(_ context.Context, _ provider.StackRef, functions []provider.Function, _ progress.Log) ([]provider.Function, error) {
					var kept []provider.Function
					for _, function := range functions {
						if !s.removeRevision(function.Revision) {
							kept = append(kept, function)
						}
					}
					return kept, nil
				},
			},
		},
		Containers: &resources.ContainerHooks{
			Provision: func(ctx context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.AppContainer, error) {
				revision, err := s.provision(spec)
				if err != nil {
					return nil, err
				}
				containers, _ := s.nameContainers(ctx, spec)
				containers[0].Revision = revision
				return containers, nil
			},
			Remove: func(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ progress.Log) error {
				for _, container := range containers {
					s.remove(container.Physical)
				}
				return nil
			},
			Shared: &resources.SharedHooks[provider.AppContainer]{
				Name: s.nameContainers,
				RemoveRevisions: func(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ progress.Log) ([]provider.AppContainer, error) {
					var kept []provider.AppContainer
					for _, container := range containers {
						if !s.removeRevision(container.Revision) {
							kept = append(kept, container)
						}
					}
					return kept, nil
				},
			},
		},
	}
}

func releaseRef(buildID string) provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
		Name:    naming.AppStack("prod", "web", naming.NewReleaseToken(buildID, "f1")),
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
	if err := r.stacks.Destroy(context.Background(), ref, nil, nil); err != nil {
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

func sharedEntries(t *testing.T, store keyvalue.Store) []keyvalue.Entry {
	t.Helper()
	shared := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootSharedPhysicals, Path: []string{"shop"}}
	left, err := store.List(context.Background(), shared)
	if err != nil {
		t.Fatal(err)
	}
	return left
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

	removed, revisions := service.removals()
	if len(removed) != 0 {
		t.Errorf("Destroy() took down %v, which the surviving release still serves from", removed)
	}
	if want := []string{"shop-prod-web-api-00001"}; !slices.Equal(revisions, want) {
		t.Errorf("Destroy() took the revisions %v, want %v: only the revision the dropped release deployed", revisions, want)
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

	if removed, revisions := service.removals(); len(removed) != 0 || len(revisions) != 0 {
		t.Errorf("Destroy() took down %v and the revisions %v, and the surviving release serves that very revision", removed, revisions)
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

	if removed, _ := service.removals(); !slices.Equal(removed, []string{"shop-prod-web-api"}) {
		t.Errorf("Destroy() of the last release took down %v, want the function nothing else names", removed)
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

	if removed, _ := service.removals(); !slices.Equal(removed, []string{"shop-prod-web-api"}) {
		t.Errorf("two destroys whose records were both still written took down %v, want the service: each saw the other's record and left the service to it", removed)
	}
	if left := sharedEntries(t, store); len(left) != 0 {
		t.Errorf("the destroys left %v behind, want nothing: a teardown leaves no bytes", left)
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

	removed, revisions := service.removals()
	if len(removed) != 0 {
		t.Errorf("Destroy() took down %v, which the surviving release still serves from", removed)
	}
	if want := []string{"shop-prod-web-00001"}; !slices.Equal(revisions, want) {
		t.Errorf("Destroy() took the revisions %v, want %v", revisions, want)
	}
}

func TestADestroyOnAProviderWhoseReleasesShareNothingReadsOnlyItsOwnStack(t *testing.T) {
	t.Parallel()

	store := &countedStore{Store: fake.NewKeyValues()}
	own := &withContainers{buckets: &buckets{}}
	released := newReleases(t, store, own.hooks())
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

	const readsPerRelease = 3
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
		if removed, _ := service.removals(); !slices.Equal(removed, []string{"shop-prod-web-api"}) {
			t.Errorf("tearing down %d releases took down %v, want the service once", count, removed)
		}
	}
}

func TestAStackWhoseProvisionFailedPartwayIsDestroyedThroughWhatItsVendorNamed(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	service := &serviceEveryReleaseRevises{revision: "shop-prod-web-api-00001", failProvision: errors.New("the revision never became ready")}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), service.hooks())
	ref := releaseRef("d1")
	if _, err := stacks.Provision(context.Background(), provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: functionApp()}, nil); err == nil {
		t.Fatal("Provision() = nil, want the vendor's failure")
	}

	if err := stacks.Destroy(context.Background(), ref, nil, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}

	if removed, _ := service.removals(); !slices.Equal(removed, []string{"shop-prod-web-api"}) {
		t.Errorf("destroying the stack whose provision failed took down %v, want the service it may have created: nothing else holds it", removed)
	}
	if left := sharedEntries(t, store); len(left) != 0 {
		t.Errorf("the destroy left %v behind", left)
	}
}

func TestADestroyWhileAnotherReleaseProvisionsLeavesTheServiceToIt(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{revision: "shop-prod-web-api-00001"}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	only, provisioning := releaseRef("d1"), releaseRef("d2")
	released.provision(only, functionApp())
	service.revision = "shop-prod-web-api-00002"
	service.onProvision = func() { released.destroy(only) }

	released.provision(provisioning, functionApp())

	if removed, _ := service.removals(); len(removed) != 0 {
		t.Errorf("destroying the only recorded release while another provisioned took down %v, want the service left to the release provisioning onto it", removed)
	}
}

func TestAProvisionWaitsForADestroyRemovingTheServiceWhole(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{revision: "shop-prod-web-api-00001"}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	only, next := releaseRef("d1"), releaseRef("d2")
	released.provision(only, functionApp())
	removing, release := make(chan struct{}), make(chan struct{})
	service.onRemove = func() {
		close(removing)
		<-release
	}
	destroyed := make(chan error, 1)
	go func() { destroyed <- released.stacks.Destroy(context.Background(), only, nil, nil) }()
	<-removing

	provisioned := make(chan error, 1)
	go func() {
		_, err := released.stacks.Provision(context.Background(), provider.StackSpec{Ref: next, Kind: provider.StackApp, App: functionApp()}, nil)
		provisioned <- err
	}()
	select {
	case err := <-provisioned:
		t.Fatalf("Provision() returned %v while the service it revises was being deleted, want it to wait for the deletion", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-destroyed; err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if err := <-provisioned; err != nil {
		t.Fatalf("Provision() after the deletion = %v", err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !slices.Contains(service.provisioned, next.Name.String()) {
		t.Errorf("the vendor provisioned %v, want %s after the deletion finished", service.provisioned, next.Name)
	}
}

func TestARevisionTheVendorKeptIsRemovedOnceANewerOneIsProvisioned(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	active, failed, next := releaseRef("d1"), releaseRef("d2"), releaseRef("d3")
	service.revision = "shop-prod-web-api-00001"
	released.provision(active, functionApp())
	service.revision = "shop-prod-web-api-00002"
	released.provision(failed, functionApp())

	released.destroy(failed)
	released.forget(failed)
	if _, revisions := service.removals(); len(revisions) != 0 {
		t.Fatalf("the destroy removed revisions %v, want the latest one the vendor refuses kept", revisions)
	}

	service.revision = "shop-prod-web-api-00003"
	released.provision(next, functionApp())

	if _, revisions := service.removals(); !slices.Equal(revisions, []string{"shop-prod-web-api-00002"}) {
		t.Errorf("after a newer revision was provisioned the vendor removed %v, want the revision it kept before, which no release holds", revisions)
	}
}

func TestAHolderWhoseStackRecordIsGoneIsRecordedAgainSoATeardownReachesIt(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	service := &serviceEveryReleaseRevises{revision: "shop-prod-web-api-00001"}
	released := newReleases(t, store, service.hooks())
	lost := releaseRef("d1")
	released.provision(lost, functionApp())
	released.forget(lost)

	restored, err := resources.RecordUnrecordedHolders(context.Background(), store, environment.TierProduction, "shop", func(naming.StackName) bool { return true })
	if err != nil {
		t.Fatalf("RecordUnrecordedHolders() = %v", err)
	}
	if want := []naming.StackName{lost.Name}; !slices.Equal(restored, want) {
		t.Fatalf("RecordUnrecordedHolders() = %v, want %v", restored, want)
	}
	released.destroy(lost)

	if removed, _ := service.removals(); !slices.Equal(removed, []string{"shop-prod-web-api"}) {
		t.Errorf("destroying the restored holder took down %v, want the service it held", removed)
	}
	if left := sharedEntries(t, store); len(left) != 0 {
		t.Errorf("the teardown left %v behind", left)
	}
}

func TestDestroyOfAStackOfSeveralFunctionsTakesThemDownInOneRemoveCall(t *testing.T) {
	t.Parallel()

	service := &serviceEveryReleaseRevises{revision: "shop-prod-web-00001"}
	released := newReleases(t, fake.NewKeyValues(), service.hooks())
	ref := releaseRef("d1")
	released.provision(ref, &provider.AppSpec{
		App: "web", Compute: provider.ComputeServerless,
		Functions: []provider.FunctionSpec{{Name: "api"}, {Name: "worker"}},
	})

	released.destroy(ref)

	service.mu.Lock()
	calls := slices.Clone(service.functionRemovals)
	service.mu.Unlock()
	want := [][]string{{"shop-prod-web-api", "shop-prod-web-worker"}}
	if len(calls) != 1 || !slices.Equal(calls[0], want[0]) {
		t.Errorf("Destroy() called the remove hook with %v, want one call naming every function the stack recorded: %v", calls, want)
	}
}
