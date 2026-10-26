package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func networkPatches(t *testing.T, b bootstrap.Engine, patches transform.Patches, tags map[string]string) network.Patches {
	t.Helper()
	return networkPatchesOf(t, b, "shop", patches, tags)
}

func networkPatchesOf(t *testing.T, b bootstrap.Engine, project string, patches transform.Patches, tags map[string]string) network.Patches {
	t.Helper()
	read, err := network.ReadPatches(context.Background(), b, environment.TierProduction, project, transform.Result{Patches: overTheWire(patches), Tags: tags})
	if err != nil {
		t.Fatalf("readPatches() = %v", err)
	}
	return read
}

func recordingNetworkPatches(t *testing.T) (bootstrap.Engine, *networkServer) {
	t.Helper()
	b, server := servingNetworks(t)
	b.Records = fake.NewKeyValues()
	return b, server
}

func everyNetworkPatch() transform.Patches {
	return transform.Patches{
		"network": {"mtu": 1500},
		"subnetwork": {
			"ipCidrRange":           "10.96.0.0/20",
			"privateIpGoogleAccess": true,
			"logConfig":             map[string]any{"flowSampling": 0.25},
			"secondaryIpRanges":     []any{map[string]any{"rangeName": "pods", "ipCidrRange": "10.100.0.0/20"}},
		},
		"connectionPolicy": {"pscConfig": map[string]any{"limit": "20"}},
	}
}

func TestATransformedPrivateNetworkIsMadeWithItsPatches(t *testing.T) {
	b, server := recordingNetworkPatches(t)
	ctx := context.Background()

	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, everyNetworkPatch(), map[string]string{"team": "platform"})); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}

	if net := server.networks["ocel-prod-network"]; net.Mtu != 1500 || net.AutoCreateSubnetworks || net.RoutingConfig.RoutingMode != "REGIONAL" {
		t.Errorf("the network is %+v, want an MTU of 1500 in custom mode with regional routing", net)
	}
	subnet := server.subnets["ocel-prod-network"]
	if subnet.IpCidrRange != "10.96.0.0/20" || !subnet.PrivateIpGoogleAccess || subnet.LogConfig == nil || !subnet.LogConfig.Enable || subnet.LogConfig.FlowSampling != 0.25 ||
		len(subnet.SecondaryIpRanges) != 1 || subnet.SecondaryIpRanges[0].RangeName != "pods" || !strings.HasSuffix(subnet.Network, "/global/networks/ocel-prod-network") {
		t.Errorf("the subnetwork is %+v, want the patched range, private Google access, flow logs at 0.25 and the pods range, on the tier's network", subnet)
	}
	for _, name := range []string{"ocel-prod-network-memorystore", "ocel-prod-network-cloudsql"} {
		policy := server.policies[name]
		if policy.PscConfig == nil || policy.PscConfig.Limit != 20 || policy.Labels["team"] != "platform" || len(policy.PscConfig.Subnetworks) != 1 {
			t.Errorf("the connection policy %s is %+v, want the tier's subnetwork, a limit of 20 and the team label", name, policy)
		}
	}
}

func TestAPatchedPrivateNetworkIsUpdatedWhereItDriftsAndLeftAloneOnceInStep(t *testing.T) {
	b, server := recordingNetworkPatches(t)
	server.installed()
	ctx := context.Background()
	patches := transform.Patches{
		"subnetwork":       {"privateIpGoogleAccess": true, "logConfig": map[string]any{}},
		"connectionPolicy": {"pscConfig": map[string]any{"limit": "20"}},
	}

	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, patches, map[string]string{"team": "platform"})); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}
	want := []string{
		"set private Google access on ocel-prod-network", "update subnetwork ocel-prod-network",
		"update policy ocel-prod-network-memorystore pscConfig.limit,labels", "update policy ocel-prod-network-cloudsql pscConfig.limit,labels",
	}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("patching the network in place wrote %v, want %v", got, want)
	}
	if logs := server.subnets["ocel-prod-network"].LogConfig; logs == nil || !logs.Enable || logs.AggregationInterval != "INTERVAL_5_SEC" || logs.FlowSampling != 0.5 {
		t.Errorf("the subnetwork's flow logs are %+v, want them on at Google's defaults", logs)
	}

	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, patches, map[string]string{"team": "platform"})); err != nil {
		t.Fatalf("makeNetwork() again = %v", err)
	}
	if got := server.wrote(); len(got) != len(want) {
		t.Errorf("patching a network already in step wrote %v, want nothing", got[len(want):])
	}
}

func TestRemovingANetworkPatchPutsItsFieldBack(t *testing.T) {
	b, server := recordingNetworkPatches(t)
	server.installed()
	ctx := context.Background()
	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, transform.Patches{
		"subnetwork":       {"privateIpGoogleAccess": true, "logConfig": map[string]any{}},
		"connectionPolicy": {"pscConfig": map[string]any{"limit": "20"}},
	}, map[string]string{"team": "platform"})); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}
	before := len(server.wrote())

	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, transform.Patches{"subnetwork": {"privateIpGoogleAccess": true}}, nil)); err != nil {
		t.Fatalf("makeNetwork() with fewer patches = %v", err)
	}

	want := []string{"update subnetwork ocel-prod-network", "update policy ocel-prod-network-memorystore pscConfig.limit,labels", "update policy ocel-prod-network-cloudsql pscConfig.limit,labels"}
	if got := server.wrote()[before:]; !slices.Equal(got, want) {
		t.Errorf("dropping the flow logs, limit and tags wrote %v, want %v", got, want)
	}
	if logs := server.subnets["ocel-prod-network"].LogConfig; logs != nil && logs.Enable {
		t.Errorf("the subnetwork's flow logs are %+v, want them off again", logs)
	}
	if policy := server.policies["ocel-prod-network-cloudsql"]; policy.PscConfig.Limit != 0 || len(policy.Labels) != 0 {
		t.Errorf("the connection policy is %+v, want no limit and no label", policy)
	}
	if !server.subnets["ocel-prod-network"].PrivateIpGoogleAccess {
		t.Error("private Google access went off, want it kept: the patch still sets it")
	}
}

func TestADeployOfAnotherProjectWithoutNetworkPatchesLeavesTheOnesTheNetworkRecords(t *testing.T) {
	for _, project := range []string{"blog", ""} {
		b, server := recordingNetworkPatches(t)
		ctx := context.Background()
		patched := networkPatches(t, b, everyNetworkPatch(), nil)
		if err := network.Make(ctx, b, environment.TierProduction, patched); err != nil {
			t.Fatalf("makeNetwork() = %v", err)
		}
		before := len(server.wrote())

		unpatched := networkPatchesOf(t, b, project, nil, nil)

		if unpatched.Digest() != patched.Digest() || network.NewPart(b, environment.TierProduction, unpatched).Digest() != network.NewPart(b, environment.TierProduction, patched).Digest() {
			t.Errorf("a deploy of %q patching nothing in the network digests it as %q, want %q: only shop, which patched it, puts it back", project, unpatched.Digest(), patched.Digest())
		}
		if err := network.Make(ctx, b, environment.TierProduction, unpatched); err != nil {
			t.Fatalf("makeNetwork() without patches = %v", err)
		}
		if got := server.wrote()[before:]; len(got) != 0 {
			t.Errorf("making the network again for %q without patches wrote %v, want nothing", project, got)
		}
		if server.subnets["ocel-prod-network"].IpCidrRange != "10.96.0.0/20" {
			t.Errorf("the subnetwork is %+v, want the patched range kept", server.subnets["ocel-prod-network"])
		}
	}
}

func TestADeployOfTheProjectThatPatchedTheNetworkWithoutPatchesPutsItsFieldsBack(t *testing.T) {
	b, server := recordingNetworkPatches(t)
	server.installed()
	ctx := context.Background()
	patched := networkPatches(t, b, transform.Patches{
		"subnetwork":       {"logConfig": map[string]any{}},
		"connectionPolicy": {"pscConfig": map[string]any{"limit": "20"}},
	}, map[string]string{"team": "platform"})
	if err := network.Make(ctx, b, environment.TierProduction, patched); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}

	unpatched := networkPatches(t, b, nil, nil)

	if unpatched.Digest() == patched.Digest() || network.NewPart(b, environment.TierProduction, unpatched).UpdateReason == "" {
		t.Fatalf("shop's deploy with its network transform removed digests the network as %q, want it made again without the patches", unpatched.Digest())
	}
	if err := network.Make(ctx, b, environment.TierProduction, unpatched); err != nil {
		t.Fatalf("makeNetwork() without patches = %v", err)
	}
	if logs := server.subnets["ocel-prod-network"].LogConfig; logs != nil && logs.Enable {
		t.Errorf("the subnetwork's flow logs are %+v, want them off again", logs)
	}
	if policy := server.policies["ocel-prod-network-cloudsql"]; policy.PscConfig.Limit != 0 || len(policy.Labels) != 0 {
		t.Errorf("the connection policy is %+v, want no limit and no label", policy)
	}
	if again := networkPatchesOf(t, b, "blog", nil, nil); again.Digest() != "" {
		t.Errorf("the network digests as %q once shop dropped its patches, want nothing recorded for another project to keep", again.Digest())
	}
}

func TestARemovedPrivateNetworkIsMadeAgainWithoutThePatchesItWasMadeWith(t *testing.T) {
	b, server := recordingNetworkPatches(t)
	ctx := context.Background()
	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, everyNetworkPatch(), nil)); err != nil {
		t.Fatalf("makeNetwork() = %v", err)
	}
	if err := network.Remove(ctx, b, environment.TierProduction); err != nil {
		t.Fatalf("removeNetwork() = %v", err)
	}

	if err := network.Make(ctx, b, environment.TierProduction, networkPatches(t, b, nil, nil)); err != nil {
		t.Fatalf("makeNetwork() again = %v", err)
	}

	if subnet := server.subnets["ocel-prod-network"]; subnet.IpCidrRange != network.SubnetRange || subnet.PrivateIpGoogleAccess {
		t.Errorf("the network made again holds %+v, want ocel's range and no private Google access: removing the network forgets the patches it was made with", subnet)
	}
}

func TestThePrivateNetworkPartNamesTheRangeAPatchGivesItsSubnetwork(t *testing.T) {
	b, _ := servingNetworks(t)

	part := network.NewPart(b, environment.TierProduction, networkPatches(t, b, transform.Patches{"subnetwork": {"ipCidrRange": "10.96.0.0/20"}}, nil))

	if !strings.Contains(part.Summary, "10.96.0.0/20") || strings.Contains(part.Summary, network.SubnetRange) {
		t.Errorf("the part says %q, want the patched range 10.96.0.0/20 named in place of %s", part.Summary, network.SubnetRange)
	}
}

func TestThePrivateNetworkPartIsMadeAgainWhenItsPatchesChange(t *testing.T) {
	b, _ := servingNetworks(t)
	unpatched := network.NewPart(b, environment.TierProduction, network.Patches{})
	patched := network.NewPart(b, environment.TierProduction, networkPatches(t, b, transform.Patches{"subnetwork": {"privateIpGoogleAccess": true}}, nil))

	if patched.Digest() == unpatched.Digest() {
		t.Error("the network part digests alike with and without patches, so a deploy that patches it never makes it again")
	}
	if !strings.Contains(patched.UpdateReason, "differ from the ones it records") || unpatched.UpdateReason != "" {
		t.Errorf("the part's update reasons are %q and %q, want the patched one to say its transforms differ", patched.UpdateReason, unpatched.UpdateReason)
	}
}

func TestAPatchedRangeOrMTUForAnExistingNetworkIsRefused(t *testing.T) {
	for _, patch := range []transform.Patches{
		{"subnetwork": {"ipCidrRange": "10.96.0.0/20"}},
		{"network": {"mtu": 1500}},
	} {
		b, server := recordingNetworkPatches(t)
		server.installed()

		err := network.Make(context.Background(), b, environment.TierProduction, networkPatches(t, b, patch, nil))

		if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "--remove private-network") {
			t.Errorf("makeNetwork() patching %v over an existing network = %v, want a refusal naming how to start the network again", patch, err)
		}
		if got := server.wrote(); len(got) != 0 {
			t.Errorf("a refused patch %v wrote %v, want nothing", patch, got)
		}
	}
}

func TestTheSubnetworkKeepsTheRangeItWasCreatedWithWhenAPatchAsksForTheSame(t *testing.T) {
	b, server := recordingNetworkPatches(t)
	server.installed()

	if err := network.Make(context.Background(), b, environment.TierProduction, networkPatches(t, b, transform.Patches{"subnetwork": {"ipCidrRange": network.SubnetRange}}, nil)); err != nil {
		t.Errorf("makeNetwork() patching the range the subnetwork holds = %v, want it admitted", err)
	}
}

func TestADeployDeclaringADatabaseMakesTheNetworkWithPatchesEvaluatedForTheTierAlone(t *testing.T) {
	p, server := deployingNetworks(t)
	pass := &patchingByKind{patches: map[string]transform.Patches{network.TransformKind: {"subnetwork": {"privateIpGoogleAccess": true}}}}
	p.transforms = pass
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
	pre := declaringADatabase("17")
	pre.Deploy.Slug, pre.Deploy.Env = "shop", "prod"

	if err := p.ApplyPreflight(context.Background(), pre); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}

	if pass.seen.Env != "" || pass.seen.EnvTier != "production" || len(pass.seen.Resources) != 1 || pass.seen.Resources[0] != (transform.Resource{Type: "network", Name: "production"}) {
		t.Errorf("the network's transforms were asked %+v, want the tier's network alone, with no env: the network belongs to every project of the tier", pass.seen)
	}
	if !server.subnets["ocel-prod-network"].PrivateIpGoogleAccess {
		t.Errorf("the subnetwork is %+v, want private Google access the transform set", server.subnets["ocel-prod-network"])
	}
}

func TestADryDeployPlansTheNetworkAgainWhenItsPatchesDifferFromTheOnesItRecords(t *testing.T) {
	p, server := deployingNetworks(t)
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
	ctx := context.Background()
	if err := p.ApplyPreflight(ctx, declaringADatabase("17")); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}
	p.transforms = &patchingByKind{patches: map[string]transform.Patches{network.TransformKind: {"subnetwork": {"privateIpGoogleAccess": true}}}}
	p.evaluatedNetworks.Clear()

	plan, err := p.PlanPreflight(ctx, declaringADatabase("17"))
	if err != nil {
		t.Fatalf("PlanPreflight() = %v", err)
	}

	if len(plan.Groups) != 1 || plan.Groups[0].Part != network.PartName || plan.Groups[0].Action != provider.ActionUpdate || !strings.Contains(plan.Groups[0].Reason, "every project in the tier") {
		t.Errorf("a dry deploy with new network patches planned %+v, want the network updated for every project in the tier", plan.Groups)
	}
	if server.subnets["ocel-prod-network"].PrivateIpGoogleAccess {
		t.Error("a dry deploy changed the subnetwork")
	}
}

func TestANetworkPatchFilledFromABindingIsRefused(t *testing.T) {
	p, server := deployingNetworks(t)
	p.transforms = &patchingByKind{patches: map[string]transform.Patches{network.TransformKind: {"connectionPolicy": {"pscConfig": map[string]any{
		"limit": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "limits", "property": "connections"}},
	}}}}}

	_, err := p.PlanPreflight(context.Background(), declaringADatabase("17"))

	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "bindings.custom.limits.connections") {
		t.Errorf("PlanPreflight() with a network patch filled from a binding = %v, want a refusal naming the binding", err)
	}
	if got := server.wrote(); len(got) != 0 {
		t.Errorf("a refused patch wrote %v", got)
	}
}

func TestAnUnusableSubnetworkRangeIsRefusedBeforeAnythingIsMade(t *testing.T) {
	for _, block := range []string{"8.8.0.0/20", "10.240.0.0/28", "10.240.0.0/8", "10.240.0.1/20", "subnet"} {
		b, server := recordingNetworkPatches(t)

		_, err := network.ReadPatches(context.Background(), b, environment.TierProduction, "shop", transform.Result{Patches: transform.Patches{"subnetwork": {"ipCidrRange": block}}})

		if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "subnetwork.ipCidrRange") {
			t.Errorf("readPatches() of ipCidrRange %s = %v, want a refusal naming the path", block, err)
		}
		if got := server.wrote(); len(got) != 0 {
			t.Errorf("an unusable range %s wrote %v", block, got)
		}
	}
}
