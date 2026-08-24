package resources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
)

type buckets struct {
	removed []providerkit.Link
}

func (b *buckets) Bucket(_ context.Context, in resources.Instruction, _ providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{
		Type:       providerkit.LinkBucket,
		Name:       in.Resource.Name,
		Properties: map[string]string{providerkit.PropertyBucket: in.Ref.Project + "-" + in.Resource.Name},
	}, nil
}

func (b *buckets) RemoveResource(_ context.Context, _ providerkit.StackRef, link providerkit.Link, _ providerkit.Reporter) error {
	b.removed = append(b.removed, link)
	return nil
}

type neon struct{ *buckets }

func (neon) Postgres(_ context.Context, in resources.Instruction, _ providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{
		Type: providerkit.LinkPostgres,
		Name: in.Resource.Name,
		Properties: map[string]string{
			providerkit.PropertyHost:     "db.neon.invalid",
			providerkit.PropertyPort:     "5432",
			providerkit.PropertyDatabase: in.Resource.Name,
			providerkit.PropertyUsername: "app",
			providerkit.PropertyPassword: "hunter2",
		},
	}, nil
}

type halfLink struct{}

func (halfLink) Postgres(_ context.Context, in resources.Instruction, _ providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{
		Type:       providerkit.LinkPostgres,
		Name:       in.Resource.Name,
		Properties: map[string]string{providerkit.PropertyHost: "db.invalid"},
	}, nil
}

func infraRef() providerkit.StackRef {
	return providerkit.StackRef{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Name:    naming.InfraStack("prod"),
	}
}

func TestServesNamesEveryPrimitiveTheProviderImplements(t *testing.T) {
	t.Parallel()

	if served := resources.Serves(&buckets{}); !slices.Equal(served, []providerkit.LinkType{providerkit.LinkBucket}) {
		t.Fatalf("Serves() = %v, want only the bucket it implements", served)
	}
	served := resources.Serves(neon{&buckets{}})
	if !slices.Contains(served, providerkit.LinkPostgres) || !slices.Contains(served, providerkit.LinkBucket) {
		t.Fatalf("Serves() = %v, want the embedded bucket and the Postgres the override adds", served)
	}
}

func TestReleaserFansEachResourceOutToItsPrimitive(t *testing.T) {
	t.Parallel()

	records := fake.NewRecords()
	releaser := resources.Releaser(records, neon{&buckets{}})

	result, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  infraRef(),
		Kind: providerkit.StackInfra,
		Resources: []providerkit.Resource{
			{Name: "orders", Type: providerkit.LinkPostgres},
			{Name: "uploads", Type: providerkit.LinkBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Links) != 2 {
		t.Fatalf("Provision() returned %d links, want one per resource", len(result.Links))
	}
	if result.Links[0].Properties[providerkit.PropertyHost] != "db.neon.invalid" {
		t.Errorf("orders came back from %q, want the override that serves Postgres", result.Links[0].Properties[providerkit.PropertyHost])
	}
}

func TestReleaserRefusesAPrimitiveNothingServes(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), &buckets{})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:       infraRef(),
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "orders", Type: providerkit.LinkPostgres}},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Provision() of a primitive nothing serves = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refusal.Message, string(providerkit.LinkBucket)) {
		t.Errorf("the refusal reads %q, want it to name what this provider does serve", refusal.Message)
	}
}

func TestReleaserRefusesALinkMissingAPropertyItsTypePromises(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), halfLink{})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:       infraRef(),
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "orders", Type: providerkit.LinkPostgres}},
	}, nil)
	if err == nil {
		t.Fatal("Provision() recorded a Postgres link carrying only a host, want it refused before anything binds to it")
	}
	if !strings.Contains(err.Error(), providerkit.PropertyDatabase) {
		t.Errorf("the refusal reads %q, want it to name the property that is missing", err)
	}
}

func TestReleaserRemovesAResourceThePlanNoLongerDeclares(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	own := &buckets{}
	releaser := resources.Releaser(records, own)
	ref := infraRef()

	if err := providerkit.WriteStack(ctx, records, ref.Class, ref.Project, ref.Name, providerkit.Stack{
		Kind: providerkit.StackInfra,
		Links: []providerkit.Link{
			{Type: providerkit.LinkBucket, Name: "uploads", Properties: map[string]string{providerkit.PropertyBucket: "shop-uploads"}},
			{Type: providerkit.LinkBucket, Name: "exports", Properties: map[string]string{providerkit.PropertyBucket: "shop-exports"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := releaser.Provision(ctx, providerkit.StackPlan{
		Ref:       ref,
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "uploads", Type: providerkit.LinkBucket}},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if len(own.removed) != 1 || own.removed[0].Name != "exports" {
		t.Fatalf("the fan-out removed %v, want only the export bucket this plan stopped declaring", own.removed)
	}
}

func TestDestroyOfAStackNothingRecordedIsANoOp(t *testing.T) {
	t.Parallel()

	own := &buckets{}
	releaser := resources.Releaser(fake.NewRecords(), own)

	if err := releaser.Destroy(context.Background(), infraRef(), nil); err != nil {
		t.Fatalf("Destroy() of a stack nothing recorded = %v, want nil", err)
	}
	if len(own.removed) != 0 {
		t.Errorf("Destroy() removed %v for a stack that was never provisioned", own.removed)
	}
}

func TestDestroyTakesDownEveryLinkTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	own := &buckets{}
	ref := infraRef()

	if err := providerkit.WriteStack(ctx, records, ref.Class, ref.Project, ref.Name, providerkit.Stack{
		Kind:  providerkit.StackInfra,
		Links: []providerkit.Link{{Type: providerkit.LinkBucket, Name: "uploads"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := resources.Releaser(records, own).Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.removed) != 1 {
		t.Fatalf("Destroy() removed %d links, want the one the stack recorded", len(own.removed))
	}
}

type embedded struct {
	*fake.Provider
	fanout providerkit.Releaser
}

func (e embedded) Releases() providerkit.Releaser { return e.fanout }

func (e embedded) Serves() []providerkit.LinkType { return resources.Serves(neon{&buckets{}}) }

func (embedded) Warm(context.Context, []string, providerkit.Reporter) error { return nil }

func TestAWarmerBehindTheFanOutIsStillFoundOnTheRoot(t *testing.T) {
	t.Parallel()

	base := fake.NewProvider(fake.Options{})
	provider := embedded{Provider: base, fanout: resources.Releaser(base.Records(), neon{&buckets{}})}

	if _, warms := providerkit.Provider(provider).(providerkit.Warmer); !warms {
		t.Fatal("the provider's Warmer is not found on the root, so wrapping the release port hid a capability")
	}
	if !slices.Contains(provider.Serves(), providerkit.LinkPostgres) {
		t.Errorf("Serves() = %v, want the Postgres the override advertises", provider.Serves())
	}
}
