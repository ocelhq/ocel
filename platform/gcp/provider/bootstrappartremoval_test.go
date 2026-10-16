package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestTheBootstrapOffersEveryPartADeployAddsAndNoEdgeRequiresOne(t *testing.T) {
	t.Parallel()

	offered := bootstrap{}.Parts()
	for _, name := range []string{networkPart, tasksPart, tagDatabasePart, albPart, envSourceSyncPart, bastionPart} {
		at := slices.IndexFunc(offered, func(part provider.Part) bool { return part.Name == name })
		if at < 0 {
			t.Errorf("Parts() = %v, want %s offered, so `ocel bootstrap <tier> --remove %s` can name it", offered, name, name)
			continue
		}
		if offered[at].Summary == "" || len(offered[at].Edges) != 0 {
			t.Errorf("%s is offered as %+v, want its summary and no edge: a deploy adds it, so no edge requires it of the bootstrap", name, offered[at])
		}
	}
	for _, kind := range []string{"alb", "cloudflare", ""} {
		if required := bootstrapplan.RequiredParts(offered, edge.Kind(kind)); slices.ContainsFunc(required, func(name string) bool { return name != cloudflareOriginPart }) {
			t.Errorf("RequiredParts(%q) = %v, want no part a deploy adds", kind, required)
		}
	}
}

func TestRemovingThePrivateNetworkByNamePlansItsResourcesAndTakesThemDown(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	server.installed()
	recordingNetwork(t, &b)
	ctx := context.Background()

	groups, err := b.planRemovedParts(ctx, environment.TierProduction, []string{networkPart}, false)
	if err != nil || len(groups) != 1 || groups[0].Part != networkPart || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 4 {
		t.Fatalf("planRemovedParts() = %+v, %v, want the network part deleted with its four resources", groups, err)
	}
	if err := b.removeNamedParts(ctx, environment.TierProduction, []string{networkPart}, false, nil); err != nil {
		t.Fatalf("removeNamedParts() = %v", err)
	}
	if len(server.networks) != 0 || len(server.policies) != 0 {
		t.Errorf("removing the network part left %v and %v", server.networks, server.policies)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, environment.TierProduction, networkPart); err != nil || present {
		t.Errorf("the part record outlived the network: %t, %v", present, err)
	}
}

func TestRemovingThePrivateNetworkByNameWhileAStoreIsOnItIsRefusedBeforeAnyPlan(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.holding(connectedStore, storeLabels(environment.TierProduction))
	recordingNetwork(t, &b)

	_, err := b.planRemovedParts(context.Background(), environment.TierProduction, []string{networkPart}, true)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "ocel-shop-prod-cache-abc123") || !strings.Contains(refused.Message, "`ocel bootstrap production --remove private-network`") {
		t.Errorf("a forced planRemovedParts() with a store on the network = %v, want a refusal naming the store and the removal to run again once it is gone: --force never cuts a store off", err)
	}
	if got := networks.wrote(); len(got) != 0 {
		t.Errorf("a refused removal wrote %v, want the network left in place", got)
	}
}

func TestARemovalSplitsThePartsADeployAddedFromTheBootstrapsOwn(t *testing.T) {
	t.Parallel()

	named, rest := splitRemovals(provider.BootstrapRequest{Remove: []string{networkPart, cloudflareOriginPart, envSourceSyncPart}})
	if !slices.Equal(named, []string{networkPart, envSourceSyncPart}) || !slices.Equal(rest.Remove, []string{cloudflareOriginPart}) {
		t.Errorf("splitRemovals() = %v, %v, want the parts a deploy added apart from %s", named, rest.Remove, cloudflareOriginPart)
	}
}

func recordingTasks(t *testing.T) bootstrap {
	t.Helper()
	b := bootstrap{clients: grantedIAM().open(t), records: recordingTopics(t, environment.TierProduction,
		provider.Binding{Type: provider.BindingTask, Name: "task--resize", Properties: map[string]string{stackrecords.PropertyDeclared: "resize"}},
	)}
	if err := stackrecords.MarkBootstrapPartReady(context.Background(), b.records, environment.TierProduction, tasksPart, stackrecords.BootstrapPart{AddedBy: "shop"}); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRemovingTheTasksPartByNameWhileATaskIsDeclaredIsRefusedUnlessForced(t *testing.T) {
	t.Parallel()
	b := recordingTasks(t)

	_, err := b.planRemovedParts(context.Background(), environment.TierProduction, []string{tasksPart}, false)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "task resize of project shop environment prod") || !strings.Contains(refused.Message, "`ocel bootstrap production --remove tasks`") {
		t.Fatalf("planRemovedParts() while a task is declared = %v, want a refusal naming the task and the removal to run again", err)
	}

	groups, err := b.planRemovedParts(context.Background(), environment.TierProduction, []string{tasksPart}, true)
	if err != nil || len(groups) != 1 || groups[0].Part != tasksPart || groups[0].Action != provider.ActionDelete {
		t.Errorf("a forced planRemovedParts() = %+v, %v, want the tasks part planned for deletion: the declared task is only who uses it, which --force overrides", groups, err)
	}
}

func TestRemovingTheBastionByNamePlansItsServiceAndAccountAndTakesThemDown(t *testing.T) {
	t.Parallel()
	target := echoTarget(t)
	h := newBastionHarness(t, target)
	h.forward(t, target)
	b := bootstrap{clients: h.b.clients, tearDown: h.b.tearDown, records: fake.NewKeyValues()}
	ctx := context.Background()

	groups, err := b.planRemovedParts(ctx, environment.TierProduction, []string{bastionPart}, false)
	if err != nil || len(groups) != 1 || groups[0].Part != bastionPart || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 2 {
		t.Fatalf("planRemovedParts() = %+v, %v, want the bastion part deleted with its service and account", groups, err)
	}
	if err := b.removeNamedParts(ctx, environment.TierProduction, []string{bastionPart}, false, nil); err != nil {
		t.Fatalf("removeNamedParts() = %v", err)
	}
	if h.run.serving() != nil {
		t.Error("the bastion service outlived its removal")
	}
}
