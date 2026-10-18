package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func boundToADatabase(spec provider.StackSpec) provider.StackSpec {
	spec.App.Values.Bindings = []provider.Binding{{Type: provider.BindingPostgres, Name: "orders"}}
	return spec
}

func productionSpec() provider.StackSpec {
	spec := previewSpec()
	spec.Ref.Tier, spec.Ref.Name.Env = environment.TierProduction, "prod"
	return spec
}

func ceilingOf(service *run.GoogleCloudRunV2Service) int64 {
	return service.Template.Scaling.MaxInstanceCount
}

func TestAFunctionBoundToAPreviewDatabaseScalesOnlyAsFarAsItsConnectionsFit(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	if _, err := p.ProvisionFunctions(context.Background(), boundToADatabase(previewSpec()), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if got := ceilingOf(server.created[len(server.created)-1]); got != 2 {
		t.Errorf("the function scales to %d instances, want 2: db-f1-micro accepts 25 connections, 3 are reserved, and each instance pools up to 10", got)
	}
}

func TestAContainerBoundToAProductionDatabaseScalesOnlyAsFarAsItsConnectionsFit(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	if _, err := p.ProvisionContainers(context.Background(), boundToADatabase(productionSpec()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if got := ceilingOf(server.created[len(server.created)-1]); got != 9 {
		t.Errorf("the container scales to %d instances, want 9: db-custom-1-3840 accepts 100 connections, 3 are reserved, and each instance pools up to 10", got)
	}
}

func TestAnAppsServicesBoundToOneDatabaseShareItsConnections(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := boundToADatabase(productionSpec())
	spec.App.Functions = append(spec.App.Functions, provider.FunctionSpec{
		Name: "fn--web--orders", Image: spec.App.Functions[0].Image, Framework: spec.App.Functions[0].Framework,
	})

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	for _, created := range slices.Concat(server.created, server.patched) {
		if got := ceilingOf(created); got != 4 {
			t.Errorf("each of two functions scales to %d instances, want 4: together they pool at most 80 of the 97 connections apps may open", got)
		}
	}
}

func TestAContainerNamingFewerInstancesThanItsDatabaseAllowsKeepsItsCount(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := boundToADatabase(productionSpec())
	spec.App.Instances = provider.Instances{Min: 1, Max: 3}

	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if got := server.created[len(server.created)-1].Template.Scaling; got.MinInstanceCount != 1 || got.MaxInstanceCount != 3 {
		t.Errorf("the container scales between %d and %d instances, want the 1 and 3 it named", got.MinInstanceCount, got.MaxInstanceCount)
	}
}

func TestAContainerAskingForMoreInstancesThanItsDatabaseAcceptsIsRefused(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := boundToADatabase(productionSpec())
	spec.App.Instances = provider.Instances{Max: 20}

	_, err := p.ProvisionContainers(context.Background(), spec, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "orders") || !strings.Contains(err.Error(), "9") {
		t.Fatalf("ProvisionContainers() asking for 20 instances = %v, want an %s refusal naming the database and the 9 it allows", err, refusal.CodeInvalid)
	}
	if len(server.created) != 0 {
		t.Errorf("released %d services before refusing, want none", len(server.created))
	}
}

func TestAnAppWithMoreServicesThanItsDatabaseHasConnectionsForIsWarned(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := boundToADatabase(previewSpec())
	for _, name := range []string{"fn--web--orders", "fn--web--search"} {
		spec.App.Functions = append(spec.App.Functions, provider.FunctionSpec{Name: name, Image: spec.App.Functions[0].Image, Framework: spec.App.Functions[0].Framework})
	}
	progress := &fake.Log{}

	if _, err := p.ProvisionFunctions(context.Background(), spec, progress); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	for _, created := range slices.Concat(server.created, server.patched) {
		if got := ceilingOf(created); got != 1 {
			t.Errorf("a function scales to %d instances, want 1, the least Cloud Run runs", got)
		}
	}
	if !slices.ContainsFunc(progress.Lines(), func(said string) bool { return strings.Contains(said, "orders") && strings.Contains(said, "30") }) {
		t.Errorf("progress said %q, want a warning that three functions at one instance each pool 30 connections, more than orders accepts", progress.Lines())
	}
}
