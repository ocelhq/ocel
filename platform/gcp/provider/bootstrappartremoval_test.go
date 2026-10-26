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
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/bastion"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func TestTheBootstrapOffersEveryPartADeployAddsAndNoEdgeRequiresOne(t *testing.T) {
	t.Parallel()

	offered := composed(bootstrap.Engine{}).Parts()
	for _, name := range []string{
		bootstrap.KeysPart, bootstrap.EdgeBucketPart, bootstrap.CacheBucketPart, network.PartName, albpart.PartName,
		bootstrap.RealtimePart, bootstrap.TopicsPart, envsource.PartName, bastion.PartName,
	} {
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
		if required := bootstrapplan.RequiredParts(offered, edge.Kind(kind)); slices.ContainsFunc(required, func(name string) bool { return name != bootstrap.CloudflareOriginPart }) {
			t.Errorf("RequiredParts(%q) = %v, want no part a deploy adds", kind, required)
		}
	}
}

func TestRemovingThePrivateNetworkByNamePlansItsResourcesAndTakesThemDown(t *testing.T) {
	b, server, _ := servingNetworksAndStores(t)
	server.installed()
	recordingNetwork(t, &b)
	ctx := context.Background()

	groups, err := b.PlanRemovedParts(ctx, environment.TierProduction, []string{network.PartName}, false, nil)
	if err != nil || len(groups) != 1 || groups[0].Part != network.PartName || groups[0].Action != provider.ActionDelete || len(groups[0].Changes) != 4 {
		t.Fatalf("planRemovedParts() = %+v, %v, want the network part deleted with its four resources", groups, err)
	}
	if err := b.RemoveNamedParts(ctx, environment.TierProduction, []string{network.PartName}, false, nil, nil); err != nil {
		t.Fatalf("removeNamedParts() = %v", err)
	}
	if len(server.networks) != 0 || len(server.policies) != 0 {
		t.Errorf("removing the network part left %v and %v", server.networks, server.policies)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, environment.TierProduction, network.PartName); err != nil || present {
		t.Errorf("the part record outlived the network: %t, %v", present, err)
	}
}

func TestRemovingThePrivateNetworkByNameWhileAStoreIsOnItIsRefusedBeforeAnyPlan(t *testing.T) {
	b, networks, stores := servingNetworksAndStores(t)
	networks.installed()
	stores.Holding(connectedStore, storeLabels(environment.TierProduction))
	recordingNetwork(t, &b)

	_, err := b.PlanRemovedParts(context.Background(), environment.TierProduction, []string{network.PartName}, true, nil)
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

	named, rest := composed(bootstrap.Engine{Removable: tierParts(network.Patches{})}).SplitRemovals(provider.BootstrapRequest{Remove: []string{network.PartName, bootstrap.CloudflareOriginPart, envsource.PartName}})
	if !slices.Equal(named, []string{network.PartName, envsource.PartName}) || !slices.Equal(rest.Remove, []string{bootstrap.CloudflareOriginPart}) {
		t.Errorf("splitRemovals() = %v, %v, want the parts a deploy added apart from %s", named, rest.Remove, bootstrap.CloudflareOriginPart)
	}
}
