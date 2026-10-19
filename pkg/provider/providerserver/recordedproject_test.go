package providerserver_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestADeployRecordsTheEdgeItWasServedByAndWhenItRan(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	options, err := structpb.NewStruct(map[string]any{"tunnel": false})
	if err != nil {
		t.Fatal(err)
	}
	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{
		Kind:          string(fake.KindRelay),
		Dns:           &contractv1.Dns{Kind: string(fake.KindZone), Zone: "shop.example"},
		AllowDegraded: []string{"isr"},
		Options:       options,
	}

	before := time.Now().Unix()
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	after := time.Now().Unix()

	project, _ := readProjectRecord(t, vendor)
	if project.Edge == nil {
		t.Fatal("the project record names no edge, want the one the deploy was served by")
	}
	if project.Edge.Kind != fake.KindRelay || project.Edge.DNSKind != string(fake.KindZone) || project.Edge.DNSZone != "shop.example" ||
		!slices.Equal(project.Edge.AllowDegraded, []string{"isr"}) {
		t.Errorf("the project record names edge %+v, want relay with DNS zone shop.example allowing isr to degrade", project.Edge)
	}
	var recorded map[string]any
	if err := json.Unmarshal(project.Edge.Options, &recorded); err != nil || recorded["tunnel"] != false {
		t.Errorf("the project record names edge options %s, want the options the deploy was served with", project.Edge.Options)
	}
	if project.UpdatedAt < before || project.UpdatedAt > after {
		t.Errorf("the project record was updated at %d, want the time of the deploy, between %d and %d", project.UpdatedAt, before, after)
	}
}

func TestADeployRecordsItsImageRegistryWithoutThePassword(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, namingARegistry(deployRequest())); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	project, _ := readProjectRecord(t, vendor)
	want := stackrecords.ProjectRegistry{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot"}
	if project.Registry == nil || *project.Registry != want {
		t.Errorf("the project record names registry %+v, want %+v", project.Registry, want)
	}
	raw, err := records.ReadOrEmpty(context.Background(), vendor.KeyValues(), stackrecords.ProjectKey(environment.TierProduction, "shop"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw.Value), "hunter2") {
		t.Errorf("the project record holds %s, want the registry's password kept out of it", raw.Value)
	}
}

func TestADeployThatNamesNoRegistryRecordsNone(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if project, _ := readProjectRecord(t, vendor); project.Registry != nil {
		t.Errorf("the project record names registry %+v, want none: the project config names no registry", project.Registry)
	}
}

func TestProvisionInfraRecordsTheEdgeOfAProjectNoDeployRecordedYet(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay), Dns: &contractv1.Dns{Kind: string(fake.KindZone), Zone: "shop.example"}}

	provisionedInfra(t, client, infraRequest(req))

	project, _ := readProjectRecord(t, vendor)
	if project.Edge == nil || project.Edge.Kind != fake.KindRelay || project.Edge.DNSZone != "shop.example" {
		t.Errorf("after ProvisionInfra() the project records edge %+v, want relay writing DNS into shop.example", project.Edge)
	}
}
