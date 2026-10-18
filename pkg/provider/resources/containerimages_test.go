package resources_test

import (
	"context"
	"errors"
	"slices"
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

func imagePushes(store provider.ImageStore) provider.ImagePushes {
	return provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{{
		App:      "web",
		Source:   testImage,
		ImageRef: "ghcr.io/acme/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Digest:   "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}}
}

func TestTheImageIsPushedBeforeTheContainerItIsProvisionedFrom(t *testing.T) {
	t.Parallel()

	own := &withContainers{buckets: &buckets{}}
	registry := fake.NewImages()
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{
		Ref:    appRef(),
		Kind:   provider.StackApp,
		App:    containerApp("web"),
		Images: imagePushes(registry),
	}

	if _, err := stacks.Provision(context.Background(), spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if pushed := registry.Pushed(); len(pushed) != 1 || pushed[0].Source != testImage {
		t.Fatalf("the fan-out pushed %v, want the app's own image", pushed)
	}
	if len(own.provisioned) != 1 {
		t.Fatalf("the fan-out provisioned %d containers, want the one the pushed image runs", len(own.provisioned))
	}
}

func TestAReleaseWhoseImageCannotBePushedProvisionsNothing(t *testing.T) {
	t.Parallel()

	own := &withContainers{buckets: &buckets{}}
	registry := fake.NewImages()
	registry.FailPushes(errors.New("the registry refused the token"))
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{
		Ref:    appRef(),
		Kind:   provider.StackApp,
		App:    containerApp("web"),
		Images: imagePushes(registry),
	}

	if _, err := stacks.Provision(context.Background(), spec, nil); err == nil {
		t.Fatal("Provision() succeeded with an image that never reached the registry, so the box would be pointed at an image it cannot pull")
	}
	if len(own.provisioned) != 0 {
		t.Fatalf("the fan-out provisioned %v after the push failed", own.provisioned)
	}
}

type retaining struct {
	*buckets
	swept        []string
	sweptWith    []provider.ImageStore
	taken        []string
	window       map[string][]string
	retained     map[string]bool
	provisioning func() error
	served       func(resources.ProvisionRequest) error
	sweeping     func() error
	forgetting   func() error
}

func (r *retaining) promote(app, imageRef string) {
	if r.window == nil {
		r.window = map[string][]string{}
	}
	if r.retained == nil {
		r.retained = map[string]bool{}
	}
	r.window[app] = append([]string{imageRef}, r.window[app]...)
	r.retained[imageRef] = true
}

func (r *retaining) hooks() resources.Hooks {
	hooks := r.buckets.hooks()
	hooks.Bucket.Provision = r.ProvisionBucket
	hooks.Containers = &resources.ContainerHooks{Provision: r.ProvisionContainers, Remove: r.RemoveContainers}
	hooks.Retention = &resources.ImageRetentionHooks{Reconcile: r.ReconcileImages, Forget: r.ForgetReleases}
	return hooks
}

func (r *retaining) retains(imageRef string) bool { return r.retained[imageRef] }

func (r *retaining) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.AppContainer, error) {
	if r.provisioning != nil {
		if err := r.provisioning(); err != nil {
			return nil, err
		}
	}
	r.promote(spec.App.App, spec.App.Image)
	return []provider.AppContainer{{Name: spec.App.App, Physical: spec.Ref.Name.String() + "-" + spec.App.App, Image: spec.App.Image}}, nil
}

func (r *retaining) RemoveContainers(_ context.Context, _ provider.StackRef, going []provider.AppContainer, _ provider.ImageStore, _ progress.Log) error {
	for _, container := range going {
		r.taken = append(r.taken, container.Physical)
	}
	return nil
}

func (r *retaining) ProvisionBucket(ctx context.Context, in resources.ProvisionRequest, progress progress.Log) (provider.Binding, error) {
	if r.served != nil {
		if err := r.served(in); err != nil {
			return provider.Binding{}, err
		}
	}
	return r.buckets.ProvisionBucket(ctx, in, progress)
}

func (r *retaining) ReconcileImages(_ context.Context, _ provider.StackRef, app, imageRef string, images provider.ImageStore, _ progress.Log) error {
	r.swept = append(r.swept, app+" "+imageRef)
	r.sweptWith = append(r.sweptWith, images)
	if r.sweeping != nil {
		if err := r.sweeping(); err != nil {
			return err
		}
	}
	for ref := range r.retained {
		if !slices.Contains(r.window[app], ref) {
			delete(r.retained, ref)
		}
	}
	return nil
}

func (r *retaining) ForgetReleases(_ context.Context, _ provider.StackRef, app string, _ provider.ImageStore, _ progress.Log) error {
	if r.forgetting != nil {
		if err := r.forgetting(); err != nil {
			return err
		}
	}
	delete(r.window, app)
	return nil
}

type refusingImages struct{ err error }

func (r refusingImages) Has(context.Context, provider.ImagePush) (bool, error) { return false, nil }

func (refusingImages) Destination() string { return "the refusing registry" }

func (refusingImages) ProbePush(context.Context, string) error { return nil }
func (refusingImages) Remove(context.Context, string) error    { return nil }

func (r refusingImages) Push(context.Context, provider.ImagePush, progress.Log) error {
	return r.err
}

func refusingPlan(err error) provider.ImagePushes {
	return provider.ImagePushes{
		Store:  refusingImages{err: err},
		Pushes: []provider.ImagePush{{App: "web", ImageRef: testImage}},
	}
}

func TestAContainerReleaseReconcilesItsImagesOnEveryPathOutOfProvision(t *testing.T) {
	t.Parallel()

	refused := errors.New("refused")
	for name, breaking := range map[string]func(*retaining) provider.StackSpec{
		"the image never lands": func(own *retaining) provider.StackSpec {
			spec := containerSpec()
			spec.Images = refusingPlan(refused)
			return spec
		},
		"a resource never provisions": func(own *retaining) provider.StackSpec {
			own.served = func(resources.ProvisionRequest) error { return refused }
			spec := containerSpec()
			spec.Resources = []provider.Resource{{Name: "store", Type: provider.BindingBucket}}
			return spec
		},
		"the container is never provisioned": func(own *retaining) provider.StackSpec {
			own.provisioning = func() error { return refused }
			return containerSpec()
		},
		"the release succeeds": func(own *retaining) provider.StackSpec {
			return containerSpec()
		},
	} {
		t.Run(name, func(t *testing.T) {
			own := &retaining{buckets: &buckets{}}
			spec := breaking(own)
			stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())

			_, err := stacks.Provision(context.Background(), spec, nil)
			if name != "the release succeeds" && err == nil {
				t.Fatal("Provision() succeeded, and this case is the failure path")
			}
			if len(own.swept) != 1 || own.swept[0] != "web "+testImage {
				t.Fatalf("the release swept %v, want one reconcile of web: the box is agentless, so a deploy that fails and is never retried must not leave it dirtier than it found it", own.swept)
			}
		})
	}
}

type unreadable struct{ *fake.KeyValues }

func (unreadable) Read(context.Context, keyvalue.Key) (keyvalue.Entry, error) {
	return keyvalue.Entry{}, errors.New("this login reads no key values in the tier")
}

func TestAContainerReleaseReconcilesEvenWhenItNeverReachedTheWork(t *testing.T) {
	t.Parallel()

	for name, breaking := range map[string]func() (keyvalue.Store, provider.StackSpec){
		"the record cannot be read": func() (keyvalue.Store, provider.StackSpec) {
			return unreadable{fake.NewKeyValues()}, containerSpec()
		},
		"a resource names a primitive this provider never serves": func() (keyvalue.Store, provider.StackSpec) {
			spec := containerSpec()
			spec.Resources = []provider.Resource{{Name: "ledger", Type: provider.BindingPostgres}}
			return fake.NewKeyValues(), spec
		},
		"the app names a compute nothing provisions": func() (keyvalue.Store, provider.StackSpec) {
			spec := containerSpec()
			spec.App.Compute = provider.Compute("steam")
			return fake.NewKeyValues(), spec
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			own := &retaining{buckets: &buckets{}}
			store, spec := breaking()
			stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

			if _, err := stacks.Provision(context.Background(), spec, nil); err == nil {
				t.Fatal("Provision() succeeded, and this case is the failure path")
			}
			if len(own.swept) != 1 || own.swept[0] != "web "+testImage {
				t.Fatalf("the release swept %v, want one reconcile of web: the sweep is deferred rather than conditional, so a release that leaves before it reaches the work sweeps its own leaked image on the way out", own.swept)
			}
		})
	}
}

func TestAServerlessReleaseReconcilesNoImages(t *testing.T) {
	t.Parallel()

	own := &retaining{buckets: &buckets{}}
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())

	spec := containerSpec()
	spec.App = nil
	if _, err := stacks.Provision(context.Background(), spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.swept) != 0 {
		t.Errorf("the release swept %v over a spec that runs no image at all", own.swept)
	}
}

func TestATeardownSweepsTheImageTheContainerItTookDownWasRetaining(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
	}); err != nil {
		t.Fatal(err)
	}

	own := &retaining{buckets: &buckets{}}
	own.promote("web", testImage)
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.swept) != 1 || own.swept[0] != "web "+testImage {
		t.Fatalf("the teardown swept %v, want the image the container it took down was the last thing referencing", own.swept)
	}
	if own.retains(testImage) {
		t.Error("the teardown left " + testImage + " on the box, and a stack that names itself in the window it never rewrites is swept by nothing that comes after it")
	}
}

func TestATeardownThatStoppedReconcilingSaysSoWithNoProgressListening(t *testing.T) {
	t.Parallel()

	refused := errors.New("the helper is not on this box")
	for name, breaking := range map[string]func(*retaining){
		"the window was never dropped": func(own *retaining) { own.forgetting = func() error { return refused } },
		"the sweep never ran":          func(own *retaining) { own.sweeping = func() error { return refused } },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := fake.NewKeyValues()
			ref := appRef()
			if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
				Kind:       provider.StackApp,
				Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
			}); err != nil {
				t.Fatal(err)
			}

			own := &retaining{buckets: &buckets{}}
			breaking(own)
			stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

			err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil)
			if !errors.Is(err, refused) {
				t.Errorf("Destroy() = %v, want %v: a destroy run with no reporter attached is where the only trace of a box that stopped reconciling would be lost", err, refused)
			}
			if !slices.Equal(own.taken, []string{"shop-prod-web"}) {
				t.Errorf("the teardown took down %v, want the container it recorded: the sweep failing is reported after the removals, never instead of them", own.taken)
			}
		})
	}
}

func containerSpec() provider.StackSpec {
	return provider.StackSpec{Ref: appRef(), Kind: provider.StackApp, App: containerApp("web")}
}

func TestATeardownThatStoppedReconcilingWarnsWhatItLeftInPlace(t *testing.T) {
	t.Parallel()

	refused := errors.New("the helper is not on this box")
	for name, tc := range map[string]struct {
		breaking func(*retaining)
		want     string
	}{
		"the window was never dropped": {
			breaking: func(own *retaining) { own.forgetting = func() error { return refused } },
			want:     "WARN Left web's release window in place: the helper is not on this box",
		},
		"the sweep never ran": {
			breaking: func(own *retaining) { own.sweeping = func() error { return refused } },
			want:     "WARN Left web's unreferenced images in place: the helper is not on this box",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := fake.NewKeyValues()
			ref := appRef()
			if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
				Kind:       provider.StackApp,
				Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
			}); err != nil {
				t.Fatal(err)
			}

			own := &retaining{buckets: &buckets{}}
			tc.breaking(own)
			stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
			progress := &fake.Log{}

			if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, progress); !errors.Is(err, refused) {
				t.Fatalf("Destroy() = %v, want %v", err, refused)
			}
			if lines := progress.Lines(); !slices.Contains(lines, tc.want) {
				t.Errorf("the teardown said %q, want %q: what a failed sweep leaves on the box is a degraded state, not tool output", lines, tc.want)
			}
		})
	}
}

func TestAReleaseHandsTheStoreItPushedToTheReconcile(t *testing.T) {
	t.Parallel()

	own := &retaining{buckets: &buckets{}}
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())
	pushed := fake.NewImages()
	spec := containerSpec()
	spec.Images = provider.ImagePushes{Store: pushed, Pushes: []provider.ImagePush{{App: "web", ImageRef: testImage}}}

	if _, err := stacks.Provision(context.Background(), spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.sweptWith) != 1 || own.sweptWith[0] != provider.ImageStore(pushed) {
		t.Errorf("the reconcile was handed %v, want the store the release pushed to: it is where the images it sweeps were sent", own.sweptWith)
	}
}

func TestATeardownHandsTheStoreItWasGivenToTheReconcile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
	}); err != nil {
		t.Fatal(err)
	}
	own := &retaining{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	given := fake.NewImages()

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref, Images: given}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.sweptWith) != 1 || own.sweptWith[0] != provider.ImageStore(given) {
		t.Errorf("the reconcile was handed %v, want the store the removal opened: the images a stack pushed live there, and a destroy that never reaches it leaves them", own.sweptWith)
	}
}

func TestATeardownGivenNoStoreReconcilesWithNone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
	}); err != nil {
		t.Fatal(err)
	}
	own := &retaining{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.sweptWith) != 1 || own.sweptWith[0] != nil {
		t.Errorf("the reconcile was handed %v, want no store", own.sweptWith)
	}
}

func TestATeardownReconcilesWithAProviderThatKeepsNoReleaseWindow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
	}); err != nil {
		t.Fatal(err)
	}
	own := &retaining{buckets: &buckets{}}
	hooks := own.hooks()
	hooks.Retention.Forget = nil

	if err := resources.NewHookStacks(store, fake.NewArtifacts(), hooks).Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.swept) != 1 {
		t.Errorf("the teardown swept %v, want one reconcile: a provider with no window to forget still has images to reclaim", own.swept)
	}
}

func TestAReleaseOfAProviderThatReconcilesNothingStillProvisions(t *testing.T) {
	t.Parallel()

	own := &retaining{buckets: &buckets{}}
	hooks := own.hooks()
	hooks.Retention.Reconcile = nil

	if _, err := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), hooks).Provision(context.Background(), containerSpec(), nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
}

type interleavedEntries struct {
	keyvalue.Store
	tier    environment.Tier
	between func()
}

func (i *interleavedEntries) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	entries, err := i.Store.List(ctx, in, under...)
	if run := i.between; run != nil && in.Tier == i.tier {
		i.between = nil
		run()
	}
	return entries, err
}

type claimedImages struct {
	*buckets
	store     keyvalue.Store
	reclaimed []string
}

func (c *claimedImages) hooks() resources.Hooks {
	hooks := c.buckets.hooks()
	hooks.Containers = &resources.ContainerHooks{
		Provision: func(context.Context, provider.StackSpec, progress.Log) ([]provider.AppContainer, error) {
			return nil, nil
		},
		Remove: c.RemoveContainers,
	}
	return hooks
}

func (c *claimedImages) RemoveContainers(ctx context.Context, ref provider.StackRef, going []provider.AppContainer, _ provider.ImageStore, _ progress.Log) error {
	kept, err := stackrecords.ListRecordedAppImages(ctx, c.store, ref.Project, ref.Name.App, ref)
	if err != nil {
		return err
	}
	for _, container := range going {
		if !kept[container.Image] {
			c.reclaimed = append(c.reclaimed, container.Image)
		}
	}
	return nil
}

func TestTwoPreviewsSharingAnImageTornDownTogetherLeaveNoImageBehind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	first := provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.AppStack("pr-7", "web", naming.NewReleaseToken("d1", "f1"))}
	second := provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: naming.AppStack("pr-8", "web", naming.NewReleaseToken("d1", "f1"))}
	for _, ref := range []provider.StackRef{first, second} {
		if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
			Kind:       provider.StackApp,
			App:        "web",
			Containers: []provider.AppContainer{{Name: "web", Physical: ref.Name.String() + "-web", Image: testImage}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	destroy := func(store keyvalue.Store, ref provider.StackRef, own *claimedImages) {
		if err := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks()).Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
			t.Errorf("Destroy(%s) = %v", ref.Name, err)
		}
		if err := stackrecords.Forget(ctx, store, ref.Tier, ref.Project, ref.Name); err != nil {
			t.Errorf("stackrecords.Forget(%s) = %v", ref.Name, err)
		}
	}
	secondOwn := &claimedImages{buckets: &buckets{}, store: store}
	interleaved := &interleavedEntries{Store: store, tier: environment.TierPreview}
	firstOwn := &claimedImages{buckets: &buckets{}, store: interleaved}
	interleaved.between = func() { destroy(store, second, secondOwn) }

	destroy(interleaved, first, firstOwn)

	if reclaimed := slices.Concat(firstOwn.reclaimed, secondOwn.reclaimed); !slices.Contains(reclaimed, testImage) {
		t.Errorf("the two teardowns reclaimed %v, want %s: each read the other's image while the other was going, and once both records are gone no stack is left to reclaim it", reclaimed, testImage)
	}
}
