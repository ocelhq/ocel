package providerserver_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func readInvalidationTargets(t *testing.T, vendor *fake.Provider, kind string) []string {
	t.Helper()
	partition := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootRouters, Path: []string{kind, "shop"}}
	entry, err := keyvalue.ReadOrEmpty(t.Context(), vendor.KeyValues(), partition.Key("invalidation"))
	if err != nil {
		t.Fatalf("read the invalidation targets: %v", err)
	}
	if len(entry.Value) == 0 {
		return nil
	}
	var targets []string
	if err := json.Unmarshal(entry.Value, &targets); err != nil {
		t.Fatalf("decode the invalidation targets %q: %v", entry.Value, err)
	}
	return targets
}

func relayDeployRequest() *contractv1.DeployRequest {
	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	return req
}

func TestADeployThroughAnEdgeThatPurgesRecordsWhereTheOriginReachesIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).PurgesAt("https://shop.acct.workers.dev/.ocel/purge")

	if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if got := readInvalidationTargets(t, vendor, string(fake.KindRelay)); !slices.Equal(got, []string{"https://shop.acct.workers.dev/.ocel/purge"}) {
		t.Errorf("invalidation targets = %v, want the edge's purge URL where the origin's invalidator reads them", got)
	}
}

func TestADeployThroughAnEdgeThatNamesNoPurgeURLRecordsNoTarget(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if got := readInvalidationTargets(t, vendor, string(fake.KindRelay)); got != nil {
		t.Errorf("invalidation targets = %v, want none: the edge names nowhere to purge", got)
	}
}

func TestARedeployRecordsTheSameTargetOnce(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).PurgesAt("https://shop.acct.workers.dev/.ocel/purge")

	for range 2 {
		if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
			t.Fatalf("Deploy() = %q", result.GetError())
		}
	}

	if got := readInvalidationTargets(t, vendor, string(fake.KindRelay)); len(got) != 1 {
		t.Errorf("invalidation targets = %v, want the one URL", got)
	}
}

func TestARedeployThatMovesTheEdgeReplacesTheTargetItRecordedBefore(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	front := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	front.PurgesAt("https://shop.old.workers.dev/.ocel/purge")
	if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	front.PurgesAt("https://shop.new.workers.dev/.ocel/purge")
	if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
		t.Fatalf("second Deploy() = %q", result.GetError())
	}

	if got := readInvalidationTargets(t, vendor, string(fake.KindRelay)); !slices.Equal(got, []string{"https://shop.new.workers.dev/.ocel/purge"}) {
		t.Errorf("invalidation targets = %v, want only the new URL", got)
	}
}

func TestRemovingAProjectForgetsItsInvalidationTarget(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).PurgesAt("https://shop.acct.workers.dev/.ocel/purge")
	if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	req := projectRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	if result := removeProject(t, client, req); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q", result.GetError())
	}

	if got := readInvalidationTargets(t, vendor, string(fake.KindRelay)); got != nil {
		t.Errorf("invalidation targets = %v, want none once the project is gone", got)
	}
}

func TestAnISRAppIsToldWhereItsEdgePurges(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index", FrameworkBuildID: "b1", ISR: true}, []byte(`{"routes":[{"id":"index"}]}`))
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).PurgesAt("https://shop.acct.workers.dev/.ocel/purge")

	if result, _ := deploy(t, client, relayDeployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	var told []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App != nil && spec.App.ISR != nil {
			told = append(told, spec.App.ISR.PurgeURL)
		}
	}
	if !slices.Equal(told, []string{"https://shop.acct.workers.dev/.ocel/purge"}) {
		t.Errorf("the ISR specs name purge URLs %v, want the edge's for the one ISR app", told)
	}
}
