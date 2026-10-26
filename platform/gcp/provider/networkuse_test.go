package gcp

import (
	"context"
	"slices"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

type patchingByKind struct {
	patches map[string]transform.Patches
	seen    transform.Request
}

func (p *patchingByKind) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	p.seen = req
	results := make([]transform.Result, len(req.Resources))
	for i, resource := range req.Resources {
		results[i] = transform.Result{Patches: overTheWire(p.patches[resource.Type])}
	}
	return results, nil
}

func placingNetworkUse(pass transform.Pass) networkUse {
	return networkUse{
		pass: pass,
		registry: component.Registry{
			{Types: []provider.BindingType{provider.BindingPostgres}, Parts: []component.Part{network.Part}, Egress: true,
				Transforms: map[provider.BindingType]*component.Transform{provider.BindingPostgres: {Kind: "postgres", Allowed: surface.Allowed{"instance": {surface.DatabasePlacement + ".pscAutoConnections.consumerNetwork"}}}}},
			{Types: []provider.BindingType{provider.BindingKV}, Parts: []component.Part{network.Part}, Egress: true},
		},
		allowed: map[string]surface.Allowed{
			"postgres":  {"instance": {surface.DatabasePlacement + ".pscAutoConnections.consumerNetwork"}},
			"container": {"service": {"template.vpcAccess.networkInterfaces.network", "template.vpcAccess.networkInterfaces.subnetwork"}},
			"function":  {"service": {"template.vpcAccess.networkInterfaces.network", "template.vpcAccess.networkInterfaces.subnetwork"}},
		},
		own: []string{"ocel-prod-network", "projects/acme-prod/global/networks/ocel-prod-network"},
	}
}

func onNetwork(network string) transform.Patches {
	return transform.Patches{"instance": {"settings": map[string]any{"ipConfiguration": map[string]any{"pscConfigs": []any{
		map[string]any{"pscAutoConnections": []any{map[string]any{"consumerNetwork": network}}},
	}}}}}
}

func egressOnto(network string) transform.Patches {
	return transform.Patches{"service": {"template": map[string]any{"vpcAccess": map[string]any{
		"networkInterfaces": []any{map[string]any{"network": network, "subnetwork": "apps"}},
	}}}}
}

func deployingOrders(apps ...string) provider.DeployPreflight {
	pre := declaringADatabase("17")
	pre.Deploy.Env = "prod"
	for _, app := range apps {
		pre.Deploy.Apps = append(pre.Deploy.Apps, provider.AppEntry{App: app, Manifest: containerApp})
		pre.Apps = append(pre.Apps, provider.AppUsage{App: app, Resources: pre.Resources})
	}
	return pre
}

func TestADeployWhoseDatabaseATransformPutsOnAnotherNetworkNeedsNoTierNetwork(t *testing.T) {
	pass := &patchingByKind{patches: map[string]transform.Patches{"postgres": onNetwork("projects/acme-prod/global/networks/shared")}}

	needed, err := placingNetworkUse(pass).needsTierNetwork(context.Background(), deployingOrders())

	if err != nil || needed {
		t.Errorf("needsTierNetwork() = %t, %v, want false: the one database is on shared and no app reaches it", needed, err)
	}
	if pass.seen.Env != "prod" || pass.seen.EnvTier != string(environment.TierProduction) || !slices.Contains(pass.seen.Resources, transform.Resource{Type: "postgres", Name: "orders"}) {
		t.Errorf("the transforms were asked %+v, want the database in its environment", pass.seen)
	}
}

func TestADeployNeedsTheTierNetworkUnlessEveryStoreAndEveryBoundAppIsPlacedOffIt(t *testing.T) {
	shared := "projects/acme-prod/global/networks/shared"
	for _, tc := range []struct {
		name   string
		pass   transform.Pass
		pre    provider.DeployPreflight
		needed bool
	}{
		{"no transforms", nil, deployingOrders(), true},
		{"a database no transform places", &patchingByKind{}, deployingOrders(), true},
		{"a database placed on the tier's own network", &patchingByKind{patches: map[string]transform.Patches{"postgres": onNetwork("ocel-prod-network")}}, deployingOrders(), true},
		{"a database placed from a binding", &patchingByKind{patches: map[string]transform.Patches{"postgres": {"instance": {"settings": map[string]any{"ipConfiguration": map[string]any{"pscConfigs": []any{
			map[string]any{"pscAutoConnections": []any{map[string]any{"consumerNetwork": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "net", "property": "id"}}}}},
		}}}}}}}, deployingOrders(), false},
		{"a store ocel cannot place", &patchingByKind{}, declaringAStore(1 << 30), true},
		{"an app bound to the database on ocel's own egress", &patchingByKind{patches: map[string]transform.Patches{"postgres": onNetwork(shared)}}, deployingOrders("web"), true},
		{"an app bound to the database whose egress a transform puts on another network", &patchingByKind{patches: map[string]transform.Patches{
			"postgres": onNetwork(shared), "container": egressOnto(shared),
		}}, deployingOrders("web"), false},
		{"an app whose egress a transform puts on the tier's own network", &patchingByKind{patches: map[string]transform.Patches{
			"postgres": onNetwork(shared), "container": egressOnto("projects/acme-prod/global/networks/ocel-prod-network"),
		}}, deployingOrders("web"), true},
		{"a database bound from another environment", &patchingByKind{patches: map[string]transform.Patches{"postgres": onNetwork(shared)}}, func() provider.DeployPreflight {
			pre := deployingOrders()
			pre.Resources[0].Binding = "orders"
			return pre
		}(), true},
	} {
		needed, err := placingNetworkUse(tc.pass).needsTierNetwork(context.Background(), tc.pre)
		if err != nil || needed != tc.needed {
			t.Errorf("%s: needsTierNetwork() = %t, %v, want %t", tc.name, needed, err, tc.needed)
		}
	}
}

func TestADeployDeclaringADatabaseStillAddsTheTierNetworkWhenNoTransformCanPlaceIt(t *testing.T) {
	p, _ := deployingNetworks(t)
	p.transforms = &patchingByKind{}

	parts, err := p.partsUsedBy(context.Background(), deployingOrders("web"))

	if err != nil || !slices.Contains(parts, network.PartName) {
		t.Errorf("partsUsedBy() = %v, %v, want the %s part", parts, err, network.PartName)
	}
}

func TestADeployNeedsTheTierNetworkUnlessEveryFunctionOfABoundAppIsPlacedOffIt(t *testing.T) {
	shared := "projects/acme-prod/global/networks/shared"
	pre := deployingOrders()
	pre.Deploy.Apps = []provider.AppEntry{{App: "web", Manifest: &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{
		Functions: []*contractv1.ManifestFunction{{LogicalName: "fn--web--index"}, {LogicalName: "fn--web--api"}},
	}}}}}
	pre.Apps = []provider.AppUsage{{App: "web", Resources: pre.Resources}}
	for _, tc := range []struct {
		name   string
		placed patchingEach
		needed bool
	}{
		{"every function placed on another network", patchingEach{"orders": onNetwork(shared), "fn--web--index": egressOnto(shared), "fn--web--api": egressOnto(shared)}, false},
		{"one function left on ocel's own egress", patchingEach{"orders": onNetwork(shared), "fn--web--index": egressOnto(shared)}, true},
	} {
		needed, err := placingNetworkUse(tc.placed).needsTierNetwork(context.Background(), pre)
		if err != nil || needed != tc.needed {
			t.Errorf("%s: needsTierNetwork() = %t, %v, want %t", tc.name, needed, err, tc.needed)
		}
	}
}

func TestADeployWhoseStoreATransformPutsOnAnotherNetworkAddsNoTierNetwork(t *testing.T) {
	p, _ := deployingNetworks(t)
	p.transforms = &patchingByKind{patches: map[string]transform.Patches{"kv": {"instance": {
		"desiredAutoCreatedEndpoints": []any{map[string]any{"network": "projects/acme-prod/global/networks/shared"}},
	}}}}
	pre := declaringAStore(1 << 30)
	pre.Deploy.Env = "prod"

	parts, err := p.partsUsedBy(context.Background(), pre)

	if err != nil || slices.Contains(parts, network.PartName) {
		t.Errorf("partsUsedBy() = %v, %v, want no %s part: the one store is on shared and no app reaches it", parts, err, network.PartName)
	}
}

func TestABoundAppWhoseTransformsMoveItsEgressOffTheTierNetworkAddsNoPartAndReachesOnlyTheNetworkItsPatchNames(t *testing.T) {
	shared := "projects/acme-prod/global/networks/shared"
	pass := &patchingByKind{patches: map[string]transform.Patches{"postgres": onNetwork(shared), "container": egressOnto(shared)}}
	deploying, _ := deployingNetworks(t)
	deploying.transforms = pass

	parts, err := deploying.partsUsedBy(context.Background(), deployingOrders("web"))
	if err != nil || slices.Contains(parts, network.PartName) {
		t.Fatalf("partsUsedBy() = %v, %v, want no %s part: the database and the app bound to it are on shared", parts, err, network.PartName)
	}

	server := &googletest.Run{}
	spec := boundToADatabase(productionSpec())
	spec.App.Compute = provider.ComputeContainer
	if err := provisionTransformedContainers(t, servingTransformed(t, server, pass), spec); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	access := server.Created[0].Template.VpcAccess
	want := &runv2.GoogleCloudRunV2VpcAccess{NetworkInterfaces: []*runv2.GoogleCloudRunV2NetworkInterface{{
		Network:    shared,
		Subnetwork: "projects/acme-prod/regions/europe-west1/subnetworks/apps",
	}}}
	if googletest.JSON(t, access) != googletest.JSON(t, want) {
		t.Errorf("released VPC access %s, want only the patch's %s: with no private-network part, nothing of the tier's network may be named", googletest.JSON(t, access), googletest.JSON(t, want))
	}
}

type countingPass struct {
	transform.Pass
	asked []transform.Request
}

func (c *countingPass) Evaluate(ctx context.Context, req transform.Request) ([]transform.Result, error) {
	c.asked = append(c.asked, req)
	return c.Pass.Evaluate(ctx, req)
}

func TestADeployAsksItsTransformsWhereItsDatabaseIsAndHowItsNetworkIsPatchedOnceEach(t *testing.T) {
	pass := &countingPass{Pass: &patchingByKind{patches: map[string]transform.Patches{"postgres": onNetwork("projects/acme-prod/global/networks/shared")}}}
	p := servingTransformed(t, &googletest.Run{}, pass)
	pre := deployingOrders()

	for range 2 {
		if _, err := p.partsUsedBy(context.Background(), pre); err != nil {
			t.Fatalf("partsUsedBy() = %v", err)
		}
		if _, err := p.evaluateNetworkPatches(context.Background(), pre.Deploy.Tier); err != nil {
			t.Fatalf("evaluateNetworkPatches() = %v", err)
		}
	}

	if len(pass.asked) != 2 {
		t.Errorf("the transforms were run %d times for %+v, want twice: once where the database is placed and once for the tier's network, whichever hook asks", len(pass.asked), pass.asked)
	}
}

type patchingWebAlone struct{ shared string }

func (p patchingWebAlone) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	results := make([]transform.Result, len(req.Resources))
	for i, resource := range req.Resources {
		switch {
		case resource.Type == "postgres":
			results[i] = transform.Result{Patches: overTheWire(onNetwork(p.shared))}
		case resource.App == "web":
			results[i] = transform.Result{Patches: overTheWire(egressOnto(p.shared))}
		}
	}
	return results, nil
}

func TestADeployNeedsTheTierNetworkWhileAnotherAppsFunctionOfTheSameNameEgressesOntoIt(t *testing.T) {
	pre := deployingOrders()
	entry := &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{
		Functions: []*contractv1.ManifestFunction{{LogicalName: "entry"}},
	}}}
	for _, app := range []string{"admin", "web"} {
		pre.Deploy.Apps = append(pre.Deploy.Apps, provider.AppEntry{App: app, Manifest: entry})
		pre.Apps = append(pre.Apps, provider.AppUsage{App: app, Resources: pre.Resources})
	}

	needed, err := placingNetworkUse(patchingWebAlone{shared: "projects/acme-prod/global/networks/shared"}).needsTierNetwork(context.Background(), pre)

	if err != nil || !needed {
		t.Errorf("needsTierNetwork() = %t, %v, want true: admin's entry function is left on ocel's own egress, whatever web's patch says", needed, err)
	}
}
