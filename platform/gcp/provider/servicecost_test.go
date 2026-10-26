package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
)

func shapedService(t *testing.T, pass transform.Pass) *costv1.Resource {
	t.Helper()
	p := pushing(t, "")
	p.transforms = pass
	web := provider.AppEntry{App: "web", Manifest: &contractv1.ManifestApp{
		Name: "web", Framework: &contractv1.Framework{Name: "node"},
		Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
	}}
	set, err := p.ShapeCost(context.Background(), provider.ShapeRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.AppEntry{web}},
		Functions: map[string][]provider.FunctionSpec{"web": {{Name: "fn--web--entry"}}},
	})
	if err != nil {
		t.Fatalf("ShapeCost() = %v", err)
	}
	for _, resource := range set.GetResources() {
		if resource.GetType() == "google_cloud_run_v2_service" && strings.HasSuffix(resource.GetScope(), "app:web") {
			return resource
		}
	}
	t.Fatalf("ShapeCost() shaped %v, want the function's Cloud Run service", set.GetResources())
	return nil
}

func TestAServiceIsPricedAtTheResourcesScalingAndBillingItsPatchSets(t *testing.T) {
	shaped := shapedService(t, servicePatched(map[string]any{"template": map[string]any{
		"containers": []any{map[string]any{"resources": map[string]any{
			"limits":  map[string]any{"cpu": "2", "memory": "4Gi"},
			"cpuIdle": false,
		}}},
		"scaling": map[string]any{"minInstanceCount": 1, "maxInstanceCount": 5},
	}}))

	template := shaped.GetProperties().AsMap()["template"].(map[string]any)
	resources := template["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)
	limits := resources["limits"].(map[string]any)
	scaling := template["scaling"].(map[string]any)
	if limits["cpu"] != "2" || limits["memory"] != "4Gi" || resources["cpu_idle"] != false {
		t.Errorf("the service is priced with resources %v, want 2 CPUs and 4Gi kept between requests", resources)
	}
	if scaling["min_instance_count"] != float64(1) || scaling["max_instance_count"] != float64(5) {
		t.Errorf("the service is priced with scaling %v, want 1 to 5 instances", scaling)
	}
}

func TestAServiceWhoseCPUABindingFillsIsPricedAsUnknown(t *testing.T) {
	shaped := shapedService(t, servicePatched(map[string]any{"template": map[string]any{
		"containers": []any{map[string]any{"resources": map[string]any{
			"limits": map[string]any{"cpu": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "cpu"}}},
		}}},
	}}))

	if !slices.Contains(shaped.GetUnknown(), "template.containers.0.resources.limits.cpu") {
		t.Errorf("the service is priced with unknown %v, want its CPU unknown until the deploy reads the record", shaped.GetUnknown())
	}
}

func TestAContainerABindingFillsAnyOfIsPricedAsUnknownWholeAndItsScalingAsPatched(t *testing.T) {
	shaped := shapedService(t, servicePatched(map[string]any{"template": map[string]any{
		"containers": []any{map[string]any{"resources": map[string]any{
			"limits": map[string]any{
				"cpu":    map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "cpu"}},
				"memory": "4Gi",
			},
		}}},
		"scaling": map[string]any{"minInstanceCount": 1, "maxInstanceCount": 5},
	}}))

	want := []string{
		"template.containers.0.resources.cpu_idle",
		"template.containers.0.resources.limits.cpu",
		"template.containers.0.resources.limits.memory",
	}
	if !slices.Equal(shaped.GetUnknown(), want) {
		t.Errorf("the service is priced with unknown %v, want %v: the containers are decoded whole at the deploy", shaped.GetUnknown(), want)
	}
	scaling := shaped.GetProperties().AsMap()["template"].(map[string]any)["scaling"].(map[string]any)
	if scaling["min_instance_count"] != float64(1) || scaling["max_instance_count"] != float64(5) {
		t.Errorf("the service is priced with scaling %v, want the 1 to 5 instances its patch sets", scaling)
	}
}

func TestScalingABindingFillsOneBoundOfIsPricedAsUnknownWhole(t *testing.T) {
	shaped := shapedService(t, servicePatched(map[string]any{"template": map[string]any{
		"scaling": map[string]any{
			"minInstanceCount": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "sizes", "property": "min"}},
			"maxInstanceCount": 5,
		},
	}}))

	want := []string{"template.scaling.max_instance_count", "template.scaling.min_instance_count"}
	if !slices.Equal(shaped.GetUnknown(), want) {
		t.Errorf("the service is priced with unknown %v, want %v: scaling is decoded whole at the deploy", shaped.GetUnknown(), want)
	}
}

type patchingApp struct {
	app   string
	patch map[string]any
}

func (p patchingApp) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	results := make([]transform.Result, len(req.Resources))
	for i, resource := range req.Resources {
		if resource.App == p.app {
			results[i] = transform.Result{Patches: overTheWire(transform.Patches{"service": p.patch})}
		}
	}
	return results, nil
}

func TestTwoAppsWithAFunctionOfTheSameNameArePricedAtTheirOwnPatches(t *testing.T) {
	p := pushing(t, "")
	p.transforms = patchingApp{app: "web", patch: map[string]any{"template": map[string]any{
		"containers": []any{map[string]any{"resources": map[string]any{"limits": map[string]any{"cpu": "4", "memory": "8Gi"}}}},
	}}}
	serverless := func(app string) provider.AppEntry {
		return provider.AppEntry{App: app, Manifest: &contractv1.ManifestApp{
			Name: app, Framework: &contractv1.Framework{Name: "node"},
			Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
		}}
	}
	set, err := p.ShapeCost(context.Background(), provider.ShapeRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.AppEntry{serverless("web"), serverless("admin")}},
		Functions: map[string][]provider.FunctionSpec{"web": {{Name: "entry"}}, "admin": {{Name: "entry"}}},
	})
	if err != nil {
		t.Fatalf("ShapeCost() = %v", err)
	}

	cpus := map[string]any{}
	for _, resource := range set.GetResources() {
		if resource.GetType() != "google_cloud_run_v2_service" {
			continue
		}
		template := resource.GetProperties().AsMap()["template"].(map[string]any)
		limits := template["containers"].([]any)[0].(map[string]any)["resources"].(map[string]any)["limits"].(map[string]any)
		cpus[resource.GetScope()[strings.LastIndex(resource.GetScope(), ":")+1:]] = limits["cpu"]
	}
	if cpus["web"] != "4" || cpus["admin"] == "4" {
		t.Errorf("the services are priced at CPUs %v, want web's at the 4 its patch sets and admin's at its own", cpus)
	}
}
