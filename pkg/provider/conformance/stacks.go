package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/realtime"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/topiclimits"
)

func declared(serves []provider.BindingType) []provider.Resource {
	resources := make([]provider.Resource, 0, len(serves))
	for _, kind := range serves {
		resource := provider.Resource{Name: "c-" + string(kind), Declared: "c-" + string(kind), Type: kind}
		switch kind {
		case provider.BindingTopic:
			resource.Topic = &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "c-consumer", Worker: "c-worker", Retry: topiclimits.ResolveRetryPolicy()}}}
		case provider.BindingTask:
			resource.Topic = &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: resource.Name, Worker: "c-worker", Exclusive: true, Retry: topiclimits.ResolveRetryPolicy()}}}
		case provider.BindingRealtime:
			resource.Realtime = &provider.RealtimeSpec{TokenTTL: realtime.DefaultTokenTTL, Channels: []provider.ChannelSpec{{
				Pattern: "orders/:orderId", Subscribe: realtime.ChannelAccessRule, Publish: realtime.ChannelAccessServer,
			}}}
		}
		resources = append(resources, resource)
	}
	return resources
}

func planRows(t *testing.T, plan provider.Plan, verb string) int {
	t.Helper()

	rows := 0
	for _, group := range plan.Groups {
		if group.Name == "" {
			t.Errorf("%s() returned %+v, and no plan can render a nameless group", verb, group)
		}
		if !provider.ValidChangeAction(group.Action) {
			t.Errorf("%s() returned group action %q, which is none the plan knows", verb, group.Action)
		}
		for _, change := range group.Changes {
			if change.Name == "" {
				t.Errorf("%s() returned %+v under %q, and no plan can render a nameless row", verb, change, group.Name)
			}
			if !provider.ValidChangeAction(change.Action) {
				t.Errorf("%s() returned change action %q, which is none the plan knows", verb, change.Action)
			}
			rows++
		}
	}
	return rows
}

const conformanceImageDigest = "4f9a2c1e0d3b5a678c9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e"

type countedImages struct {
	mu     sync.Mutex
	pushed int
}

func conformanceImage() provider.ImagePush {
	return provider.ImagePush{
		App:      "conformance",
		Source:   "ocel/conformance@sha256:" + conformanceImageDigest,
		ImageRef: "registry.invalid/conformance:sha256-" + conformanceImageDigest,
		Digest:   "sha256:" + conformanceImageDigest,
	}
}

func (c *countedImages) Destination() string { return "the counted store" }

func (c *countedImages) Has(context.Context, provider.ImagePush) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pushed > 0, nil
}

func (c *countedImages) ProbePush(context.Context, string) error { return nil }
func (c *countedImages) Remove(context.Context, string) error    { return nil }

func (c *countedImages) Push(_ context.Context, _ provider.ImagePush, _ progress.Log) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pushed++
	return nil
}

func (c *countedImages) Pushed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pushed
}

func RunStacks(t *testing.T, facts provider.Facts, stacks provider.Stacks, artifacts provider.ArtifactStore, store records.Store, vendorRoots ...records.Root) {
	t.Helper()

	ctx := context.Background()
	ref := provider.StackRef{
		Project: "conformance",
		Tier:    environment.TierPreview,
		Name:    naming.InfraStack("conformance"),
	}

	t.Run("a stack's plans and provisions agree from its creation to its teardown", func(t *testing.T) {
		runStackLifecycle(t, facts, stacks, store, vendorRoots)
	})

	t.Run("a second Provision of a stack another Provision is inside is refused as busy", func(t *testing.T) {
		runConcurrentProvisions(t, facts, stacks, store)
	})

	t.Run("Destroy of a stack that was never provisioned is a no-op", func(t *testing.T) {
		absent := ref
		absent.Name = naming.InfraStack("never-provisioned")
		if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: absent}, nil); err != nil {
			t.Fatalf("Destroy() of an absent stack = %v, want nil so a rerun of a teardown is safe", err)
		}
	})

	t.Run("Plan says what a provision would do before it does it", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		planned, err := stacks.Plan(ctx, provider.StackSpec{
			Ref:       ref,
			Kind:      provider.StackInfra,
			Recorded:  true,
			Resources: resources,
		}, nil)
		if err != nil {
			t.Fatalf("Plan() of every primitive this provider serves = %v", err)
		}
		if rows := planRows(t, planned, "Plan"); rows == 0 {
			t.Fatal("Plan() of a release provisioning every primitive showed nothing, and the plan is the only thing a human consents to")
		}
	})

	t.Run("an image the release declares is a row the plan shows and a push the apply makes", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		bare := provider.StackSpec{Ref: ref, Kind: provider.StackInfra, Recorded: true, Resources: resources}
		without, err := stacks.Plan(ctx, bare, nil)
		if err != nil {
			t.Fatalf("Plan() of a release pushing no image = %v", err)
		}

		store := &countedImages{}
		pushing := bare
		pushing.Images = provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{conformanceImage()}}
		with, err := stacks.Plan(ctx, pushing, nil)
		if err != nil {
			requireInvalid(t, err, "Plan")
			if _, err := stacks.Provision(ctx, pushing, nil); err == nil {
				t.Error("Plan() refused a declared image push and Provision() took it, so the apply pushed nothing and said so to nobody")
			}
			return
		}
		if planRows(t, with, "Plan") <= planRows(t, without, "Plan") {
			t.Error("declaring an image push added no plan row, and a push is a write to the customer's registry like any other: " +
				"the plan and the apply must ship it down one path")
		}
		if pushed := store.Pushed(); pushed != 0 {
			t.Errorf("Plan() pushed %d images, and a plan is the diff a human consents to before anything moves", pushed)
		}

		if _, err := stacks.Provision(ctx, pushing, nil); err != nil {
			t.Fatalf("Provision() of the release whose plan showed the image = %v", err)
		}
		if pushed := store.Pushed(); pushed != 1 {
			t.Errorf("Provision() pushed %d images, want the one the plan showed: the plan promised a push the apply never made", pushed)
		}
	})

	t.Run("every binding a plan asks for comes back with the properties its type promises", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a plan can ask for nothing")
		}
		result, err := stacks.Provision(ctx, provider.StackSpec{
			Ref:       ref,
			Kind:      provider.StackInfra,
			Recorded:  true,
			Resources: resources,
		}, nil)
		if err != nil {
			t.Fatalf("Provision() of every primitive this provider serves = %v", err)
		}
		if len(result.Bindings) != len(resources) {
			t.Fatalf("Provision() returned %d bindings for %d resources, and an app binds to each by name", len(result.Bindings), len(resources))
		}
		for _, binding := range result.Bindings {
			if err := provider.VerifyProperties(binding); err != nil {
				t.Errorf("Provision() returned a binding providerserver refuses to record: %v", err)
			}
			if binding.Type == provider.BindingCustom {
				continue
			}
			declared := append(stackrecords.ListRecordedProperties(binding.Type), listRedactedProperties(binding.Type)...)
			for name := range binding.Properties {
				if !slices.Contains(declared, name) {
					t.Errorf("Provision() returned binding %s with %q, which bindings.proto does not declare and the stack record does not keep, so no app and no later deploy ever reads it", binding.Name, name)
				}
			}
		}

		if store != nil {
			recorded := stackrecords.Stack{Kind: provider.StackInfra, Bindings: result.Bindings}
			if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, recorded); err != nil {
				t.Fatalf("recording what the release returned, as providerserver does after every Provision() = %v", err)
			}
			defer func() {
				if err := stackrecords.Forget(ctx, store, ref.Tier, ref.Project, ref.Name); err != nil {
					t.Errorf("forgetting the stack the teardown took = %v", err)
				}
			}()
			raw, err := records.ReadOrEmpty(ctx, store, stackrecords.StackKey(ref.Tier, ref.Project, ref.Name))
			if err != nil {
				t.Fatalf("reading the stack record back = %v", err)
			}
			for _, fault := range findRecordedSecrets(raw.Value) {
				t.Error(fault)
			}
		}

		removal, err := stacks.PlanDestroy(ctx, ref, nil)
		if err != nil {
			t.Fatalf("PlanDestroy() of the stack just provisioned = %v", err)
		}
		if rows := planRows(t, removal, "PlanDestroy"); rows == 0 {
			t.Error("PlanDestroy() of a provisioned stack showed nothing going, and a teardown is consented to by what it shows")
		}
		for _, group := range removal.Groups {
			for _, change := range group.Changes {
				if change.Action != provider.ActionDelete && change.Action != provider.ActionDisableThenDelete {
					t.Errorf("PlanDestroy() shows %s as %q, and a teardown takes everything down", change.Name, change.Action)
				}
			}
		}

		if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
			t.Fatalf("Destroy() of the stack just provisioned = %v", err)
		}
	})

	t.Run("a refusal names a code the CLI can render", func(t *testing.T) {
		unserved := provider.StackSpec{
			Ref:       ref,
			Kind:      provider.StackInfra,
			Recorded:  true,
			Resources: []provider.Resource{{Name: "unserved", Type: "no-such-primitive"}},
		}
		result, err := stacks.Provision(ctx, unserved, nil)
		if err == nil {
			if derr := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); derr != nil {
				t.Error(derr)
			}
			t.Fatalf("Provision() of %q, a primitive the contract has no type for, returned %+v and refused nothing, so a release reads as done where nothing a deploy can bind to was provisioned",
				unserved.Resources[0].Type, result.Bindings)
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			t.Fatalf("Provision() of a primitive this provider does not serve failed with %v, want a Refusal the CLI can render", err)
		}
		if !slices.Contains(
			[]refusal.Code{refusal.CodeInvalid, refusal.CodeNotReady, refusal.CodeDenied, refusal.CodeBusy},
			refused.Code,
		) {
			t.Errorf("Provision() refused with code %q, which is none providerserver maps", refused.Code)
		}
		if planned, err := stacks.Plan(ctx, unserved, nil); err == nil {
			t.Errorf("Plan() showed %+v for a release its own provision refuses, and the plan is the diff the apply runs", planned.Groups)
		}
	})
}

func listRedactedProperties(t provider.BindingType) []string {
	field := (&bindingsv1.Binding{}).ProtoReflect().Descriptor().Oneofs().ByName("properties").Fields().ByName(protoreflect.Name(t))
	if field == nil || field.Message() == nil {
		return nil
	}
	var names []string
	properties := field.Message().Fields()
	for i := range properties.Len() {
		if options, _ := properties.Get(i).Options().(*descriptorpb.FieldOptions); options.GetDebugRedact() {
			names = append(names, properties.Get(i).JSONName())
		}
	}
	return names
}

func findRecordedSecrets(raw []byte) []string {
	var recorded stackrecords.Stack
	if err := json.Unmarshal(raw, &recorded); err != nil {
		return []string{fmt.Sprintf("the stack record does not decode as stackrecords reads it: %v", err)}
	}
	var faults []string
	for _, binding := range recorded.Bindings {
		for _, name := range listRedactedProperties(binding.Type) {
			if _, held := binding.Properties[name]; held {
				faults = append(faults, fmt.Sprintf("the stack record holds binding %s's %q in clear, which bindings.proto marks debug_redact, and the record store is readable without the sealer", binding.Name, name))
			}
		}
	}
	return faults
}
