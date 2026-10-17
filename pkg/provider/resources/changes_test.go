package resources_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type changeLog struct {
	progress.Log
	changes []string
}

func newChangeLog() *changeLog { return &changeLog{Log: progress.Discard()} }

func (c *changeLog) Span(_ string, _, _ time.Time, err error, attrs ...progress.Attr) {
	values := map[progress.AttrKey]string{}
	for _, attr := range attrs {
		values[attr.Key] = attr.Value
	}
	if values[progress.AttrKeyResourceAction] == "" && err == nil {
		return
	}
	change := values[progress.AttrKeyResourceAction] + " " + values[progress.AttrKeyResourceType] + " " + values[progress.AttrKeyResourceName]
	if err != nil {
		change = "failed " + change
	}
	c.changes = append(c.changes, change)
}

func recordBuckets(t *testing.T, store keyvalue.Store, ref provider.StackRef, names ...string) {
	t.Helper()
	bindings := make([]provider.Binding, 0, len(names))
	for _, name := range names {
		bindings = append(bindings, provider.Binding{Type: provider.BindingBucket, Name: name, Properties: map[string]string{provider.PropertyBucket: "shop-" + name}})
	}
	if err := stackrecords.Write(context.Background(), store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{Kind: provider.StackInfra, Bindings: bindings}); err != nil {
		t.Fatal(err)
	}
}

func bucketSpec(ref provider.StackRef, names ...string) provider.StackSpec {
	spec := provider.StackSpec{Ref: ref, Kind: provider.StackInfra}
	for _, name := range names {
		spec.Resources = append(spec.Resources, provider.Resource{Name: name, Type: provider.BindingBucket})
	}
	return spec
}

func TestProvisionReportsEveryResourceItCreatedAndNoneItAlreadyHeld(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	ref := infraRef()
	recordBuckets(t, store, ref, "uploads")

	reported := newChangeLog()
	if _, err := resources.NewHookStacks(store, fake.NewArtifacts(), (&buckets{}).hooks()).
		Provision(context.Background(), bucketSpec(ref, "uploads", "logs"), reported); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if want := []string{"create bucket logs"}; !slices.Equal(reported.changes, want) {
		t.Errorf("Provision() reported changes %q, want %q", reported.changes, want)
	}
}

type failingBuckets struct{ failing string }

func (f failingBuckets) ProvisionBucket(ctx context.Context, in resources.ProvisionRequest, log progress.Log) (provider.Binding, error) {
	if in.Resource.Name == f.failing {
		return provider.Binding{}, errors.New("BucketAlreadyExists")
	}
	return (&buckets{}).ProvisionBucket(ctx, in, log)
}

func TestProvisionReportsTheResourceWhoseProvisioningFailed(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		recorded []string
		want     []string
	}{
		"one it was creating": {want: []string{"create bucket uploads", "failed create bucket logs"}},
		"one it already held": {recorded: []string{"uploads", "logs"}, want: []string{"failed  bucket logs"}},
	} {
		store := fake.NewKeyValues()
		ref := infraRef()
		recordBuckets(t, store, ref, test.recorded...)

		reported := newChangeLog()
		hooks := resources.Hooks{ProvisionBucket: failingBuckets{failing: "logs"}.ProvisionBucket}
		if _, err := resources.NewHookStacks(store, fake.NewArtifacts(), hooks).
			Provision(context.Background(), bucketSpec(ref, "uploads", "logs"), reported); err == nil {
			t.Fatalf("%s: Provision() = nil, want the hook's failure", name)
		}
		if !slices.Equal(reported.changes, test.want) {
			t.Errorf("%s: Provision() reported changes %q, want %q", name, reported.changes, test.want)
		}
	}
}

func TestProvisionAndDestroyReportEveryResourceTheyTookDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := infraRef()
	store := fake.NewKeyValues()
	recordBuckets(t, store, ref, "uploads", "exports")
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), (&buckets{}).hooks())

	provisioned := newChangeLog()
	if _, err := stacks.Provision(ctx, bucketSpec(ref, "uploads"), provisioned); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if want := []string{"delete bucket exports"}; !slices.Equal(provisioned.changes, want) {
		t.Errorf("Provision() reported changes %q, want %q", provisioned.changes, want)
	}

	destroyed := newChangeLog()
	if err := stacks.Destroy(ctx, ref, nil, destroyed); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if want := []string{"delete bucket uploads", "delete bucket exports"}; !slices.Equal(destroyed.changes, want) {
		t.Errorf("Destroy() reported changes %q, want %q", destroyed.changes, want)
	}
}

func TestProvisionAndDestroyReportEveryAppContainerTheyCreatedAndTookDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordContainers(t, store, ref, "legacy")
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), (&withContainers{buckets: &buckets{}}).hooks())

	provisioned := newChangeLog()
	if _, err := stacks.Provision(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: containerApp("web")}, provisioned); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if want := []string{"delete app container legacy", "create app container web"}; !slices.Equal(provisioned.changes, want) {
		t.Errorf("Provision() reported changes %q, want %q", provisioned.changes, want)
	}

	destroyed := newChangeLog()
	if err := stacks.Destroy(ctx, ref, nil, destroyed); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if want := []string{"delete app container legacy"}; !slices.Equal(destroyed.changes, want) {
		t.Errorf("Destroy() reported changes %q, want %q", destroyed.changes, want)
	}
}

func TestProvisionReportsEveryFunctionItCreatedAndTookDown(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	ref := appRef()
	recordFunctions(t, store, ref, "api", "cron")

	reported := newChangeLog()
	app := &provider.AppSpec{App: "web", Compute: provider.ComputeServerless, Functions: []provider.FunctionSpec{{Name: "api"}, {Name: "images"}}}
	if _, err := resources.NewHookStacks(store, fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks()).
		Provision(context.Background(), provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: app}, reported); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if want := []string{"delete function cron", "create function images"}; !slices.Equal(reported.changes, want) {
		t.Errorf("Provision() reported changes %q, want %q", reported.changes, want)
	}
}
