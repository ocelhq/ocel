package conformance

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const updatedOrigin = "https://conformance.example.com"

type stackLifecycle struct {
	stacks      provider.Stacks
	store       records.Store
	vendorRoots []records.Root
	ref         provider.StackRef
}

func runStackLifecycle(t *testing.T, facts provider.Facts, stacks provider.Stacks, store records.Store, vendorRoots []records.Root) {
	t.Helper()

	resources := declared(facts.Bindings)
	if len(resources) == 0 {
		t.Skip("this provider serves no resource primitive, so a release asks for nothing")
	}
	ctx := context.Background()
	life := stackLifecycle{stacks: stacks, store: store, vendorRoots: vendorRoots, ref: provider.StackRef{
		Project: "conformance",
		Tier:    environment.TierPreview,
		Name:    naming.InfraStack("lifecycle"),
	}}
	created := provider.StackSpec{Ref: life.ref, Kind: provider.StackInfra, Recorded: true, Resources: resources}
	torn := false
	defer func() {
		if !torn {
			life.destroy(t, ctx)
		}
	}()

	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"a first provision does what its plan showed", func(t *testing.T) {
			if planned := life.provisionAsPlanned(t, ctx, created); len(planned) == 0 {
				t.Error("Plan() of a stack nothing has provisioned shows nothing written, and an apply from here creates every resource it declares")
			}
		}},
		{"a second identical provision plans and does nothing", func(t *testing.T) {
			if planned := life.plan(t, ctx, created); len(planned) != 0 {
				t.Errorf("Plan() of the spec just provisioned shows %+v written, and a rerun of a deploy that changed nothing must plan all-keep", planned)
			}
			if reported := life.provision(t, ctx, created); len(reported) != 0 {
				t.Errorf("Provision() of the spec just provisioned reported %+v changed, and a rerun of a deploy that changed nothing changes nothing", reported)
			}
		}},
		{"an update does what its plan showed and then plans nothing", func(t *testing.T) {
			updated, changes := withUpdatedBucket(created)
			if !changes {
				t.Skip("this provider serves no bucket, the one primitive whose update this suite declares")
			}
			if planned := life.provisionAsPlanned(t, ctx, updated); len(planned) == 0 {
				t.Errorf("Plan() of a bucket newly allowing %s shows nothing written, so the apply either leaves the bucket as it was or changes it unconsented", updatedOrigin)
			}
			if planned := life.plan(t, ctx, updated); len(planned) != 0 {
				t.Errorf("Plan() after the update was provisioned still shows %+v written, so the provision did not do what its plan showed", planned)
			}
		}},
		{"a stack torn down plans no teardown", func(t *testing.T) {
			torn = true
			life.destroy(t, ctx)
			removal, err := stacks.PlanDestroy(ctx, life.ref, nil)
			if err != nil {
				t.Fatalf("PlanDestroy() of a stack just destroyed = %v", err)
			}
			if rows := planRows(t, removal, "PlanDestroy"); rows != 0 {
				t.Errorf("PlanDestroy() of a stack just destroyed shows %+v, and a teardown that reran would take down what is already gone", removal.Groups)
			}
		}},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			return
		}
	}
}

func withUpdatedBucket(spec provider.StackSpec) (provider.StackSpec, bool) {
	updated := spec
	updated.Resources = slices.Clone(spec.Resources)
	at := slices.IndexFunc(updated.Resources, func(resource provider.Resource) bool { return resource.Type == provider.BindingBucket })
	if at < 0 {
		return spec, false
	}
	updated.Resources[at].Bucket = &provider.BucketSpec{AllowedOrigins: []string{updatedOrigin}}
	return updated, true
}

func (l stackLifecycle) plan(t *testing.T, ctx context.Context, spec provider.StackSpec) []writtenRow {
	t.Helper()

	before := l.snapshot(t, ctx)
	planned, err := l.stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	planRows(t, planned, "Plan")
	if after := l.snapshot(t, ctx); !maps.Equal(before, after) {
		t.Errorf("Plan() changed the records a provider keeps in its key-value store, and a plan is the diff a human consents to before anything moves:\nbefore %v\nafter  %v", before, after)
	}
	return listPlannedWrites(planned)
}

func (l stackLifecycle) provisionAsPlanned(t *testing.T, ctx context.Context, spec provider.StackSpec) []writtenRow {
	t.Helper()

	planned := l.plan(t, ctx, spec)
	if reported := l.provision(t, ctx, spec); !slices.Equal(reported, planned) {
		t.Errorf("Provision() reported writing %+v, and the plan it was consented by showed %+v: the plan is the diff the apply runs", reported, planned)
	}
	return planned
}

func (l stackLifecycle) provision(t *testing.T, ctx context.Context, spec provider.StackSpec) []writtenRow {
	t.Helper()

	reported := &reportedChanges{}
	result, err := l.stacks.Provision(ctx, spec, reported)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if l.store != nil {
		if err := stackrecords.Write(ctx, l.store, l.ref.Tier, l.ref.Project, l.ref.Name, stackrecords.Stack{Kind: provider.StackInfra, Bindings: result.Bindings}); err != nil {
			t.Fatalf("recording what the release returned, as providerserver does after every Provision() = %v", err)
		}
	}
	return reported.listWrites()
}

func (l stackLifecycle) destroy(t *testing.T, ctx context.Context) {
	t.Helper()

	if err := l.stacks.Destroy(ctx, provider.StackDestroy{Ref: l.ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if l.store != nil {
		if err := stackrecords.Forget(ctx, l.store, l.ref.Tier, l.ref.Project, l.ref.Name); err != nil {
			t.Fatalf("forgetting the stack the teardown took, as providerserver does after every Destroy() = %v", err)
		}
	}
}

func (l stackLifecycle) snapshot(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()

	if l.store == nil {
		return nil
	}
	tier, project := l.ref.Tier, l.ref.Project
	partitions := []records.Partition{
		stackrecords.ProjectsPartition(tier),
		stackrecords.StacksPartition(tier, project),
		stackrecords.EnvironmentsPartition(tier, project),
		stackrecords.EdgeStacksPartition(tier),
		{Tier: tier, Root: stackrecords.RootBootstrap},
		{Tier: tier, Root: resources.RootSharedPhysicals, Path: []string{project}},
	}
	for _, root := range l.vendorRoots {
		partitions = append(partitions, records.Partition{Tier: tier, Root: root})
	}
	recorded := map[string]string{}
	for _, partition := range partitions {
		entries, err := l.store.List(ctx, partition)
		if err != nil {
			t.Fatalf("List(%s) = %v", partition, err)
		}
		for _, entry := range entries {
			recorded[entry.Key.String()] = string(entry.Revision) + " " + string(entry.Value)
		}
	}
	return recorded
}
