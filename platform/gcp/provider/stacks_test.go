package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

var sharedPreviewLabel = edge.PreviewKey("0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0").Sign("shop", "abcdefghijklmnop")

func previewSpec(label string) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierPreview,
			Name:    naming.StackName{Env: "pr-7", App: "web"},
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             "web",
			Compute:         provider.ComputeContainer,
			Image:           "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
			HealthCheckPath: "/",
			PreviewLabel:    label,
			Functions: []provider.FunctionSpec{{
				Name:      "fn--web--checkout",
				Image:     "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:abc",
				Framework: buildoutput.Framework{Name: "nodejs", Arch: string(arch.X8664)},
			}},
		},
	}
}

func TestAPreviewOnTheSharedWildcardDeploysTheServiceItsHostnameNames(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	label := sharedPreviewLabel

	containers, err := p.ProvisionContainers(context.Background(), previewSpec(label), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(containers) != 1 || containers[0].Physical != label {
		t.Fatalf("ProvisionContainers() deployed %+v, want the service named %q: the load balancer resolves the hostname's label "+
			"to a Cloud Run service of that name and nothing routes it anywhere else", containers, label)
	}
}

func TestAPreviewFunctionIsNamedApartFromThePreviewItShipsIn(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	label := sharedPreviewLabel

	functions, err := p.ProvisionFunctions(context.Background(), previewSpec(label), nil)
	if err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if len(functions) != 1 {
		t.Fatalf("ProvisionFunctions() = %+v, want the one function the spec names", functions)
	}
	if functions[0].Physical == label {
		t.Errorf("the function is served by %q, which is the service the preview's own hostname resolves to", label)
	}
	if !strings.HasPrefix(functions[0].Physical, label) {
		t.Errorf("the function is served by %q, want it under the preview's label %q so removing the preview names it", functions[0].Physical, label)
	}
}

func TestAPreviewOnAnEdgeThatShieldsNothingIsSaidToBeOpenToAnyoneWithItsUrl(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	progress := &fake.Log{}

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(""), progress); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if !slices.ContainsFunc(progress.Lines(), func(said string) bool {
		return strings.HasPrefix(said, "WARN ") && strings.Contains(said, "is a preview and answers anyone")
	}) {
		t.Errorf("the release said %q, want it to warn the preview answers anyone with its url: Cloud Run has no invoker a browser could satisfy, so the reader has to know", progress.Lines())
	}

	production := &fake.Log{}
	spec := previewSpec("")
	spec.Ref.Tier = environment.TierProduction
	spec.Ref.Name = naming.StackName{Env: stackrecords.ProductionEnv, App: "web"}
	if _, err := p.ProvisionContainers(context.Background(), spec, production); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if slices.ContainsFunc(production.Lines(), func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("a production release said %q, and production is meant to answer anyone", production.Lines())
	}

	shielded := &fake.Log{}
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec = previewSpec(sharedPreviewLabel)
	spec.Edge = front
	if _, err := p.ProvisionContainers(context.Background(), spec, shielded); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if slices.ContainsFunc(shielded.Lines(), func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("a preview behind the load balancer said %q, and its service takes traffic from the load balancer alone", shielded.Lines())
	}
}

func TestAProductionReleaseIsNamedNoDifferentlyForHavingNoPreviewLabel(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := previewSpec("")
	spec.Ref.Tier = environment.TierProduction
	spec.Ref.Name = naming.StackName{Env: stackrecords.ProductionEnv, App: "web"}

	containers, err := p.ProvisionContainers(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	want, err := names(t, p).Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0].Physical != want {
		t.Errorf("ProvisionContainers() deployed %+v, want %q: production is named for the namespace, project, environment and app as it always was",
			containers, want)
	}
}

func TestAFunctionGetsWhatTheDeployDeliveredAndWhatItsSpecNames(t *testing.T) {
	values, err := mergedValues("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"STAGE": "one"})
	if err != nil {
		t.Fatalf("mergedValues() = %v", err)
	}
	if values["DATABASE_URL"] != "postgres://" || values["STAGE"] != "one" {
		t.Errorf("mergedValues() = %v, want both what the deploy resolved and what the spec named", values)
	}
}

func TestASpecThatNamesWhatTheDeployDeliveredIsRefused(t *testing.T) {
	_, err := mergedValues("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"DATABASE_URL": "sqlite://"})
	if err == nil {
		t.Fatal("mergedValues() let the spec take the delivered value's place, and the app would read a value nothing in it declared")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("mergedValues() code = %v, want %v", code, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("mergedValues() = %v, want the name that would be displaced said", err)
	}
	if !strings.Contains(err.Error(), "fn") {
		t.Errorf("mergedValues() = %v, want what sets it said", err)
	}
}

func functionRelease(deploymentID, image string) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewRelease(deploymentID, "f1")),
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:     "web",
			Compute: provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{
				Name:      "fn--web--checkout",
				Image:     image,
				Framework: buildoutput.Framework{Name: "nodejs", Arch: string(arch.X8664)},
			}},
		},
	}
}

type releasedStacks struct {
	stacks provider.Stacks
	store  keyvalue.Store
}

func newReleasedStacks(p *Provider) releasedStacks {
	store := fake.NewKeyValues()
	return releasedStacks{stacks: resources.NewHookStacks(store, resources.NoArtifacts{}, p.resourceHooks()), store: store}
}

func (r releasedStacks) provision(t *testing.T, spec provider.StackSpec) provider.Function {
	t.Helper()
	ctx := context.Background()
	result, err := r.stacks.Provision(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Provision(%s) = %v", spec.Ref.Name, err)
	}
	if err := stackrecords.Write(ctx, r.store, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name,
		stackrecords.Stack{Kind: provider.StackApp, Functions: result.Functions}); err != nil {
		t.Fatal(err)
	}
	if len(result.Functions) != 1 {
		t.Fatalf("Provision(%s) deployed %+v, want the one function its spec names", spec.Ref.Name, result.Functions)
	}
	return result.Functions[0]
}

func (r releasedStacks) destroy(t *testing.T, ref provider.StackRef) {
	t.Helper()
	ctx := context.Background()
	if err := r.stacks.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy(%s) = %v", ref.Name, err)
	}
	if err := stackrecords.Forget(ctx, r.store, ref.Tier, ref.Project, ref.Name); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimingADroppedFunctionReleaseLeavesTheServiceServingTheActiveRelease(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	dropped := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
	gone := released.provision(t, dropped)
	active := released.provision(t, functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two"))
	if gone.Physical != active.Physical {
		t.Fatalf("the releases deployed %q and %q, want one Cloud Run service per function that every release revises", gone.Physical, active.Physical)
	}
	if err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	released.destroy(t, dropped.Ref)

	service := server.serving()
	if service == nil {
		t.Fatalf("reclaiming the dropped release deleted Cloud Run service %s, which the active release serves from", active.Physical)
	}
	if !servedBy(service.Traffic, active.Revision) {
		t.Errorf("the service serves %+v after the reclaim, want all of it still on the active release's %s", service.Traffic, active.Revision)
	}
	if revisions := server.standing(); !slices.Equal(revisions, []string{active.Revision}) {
		t.Errorf("the service keeps revisions %v after the reclaim, want only %s: the dropped release's revision is its own to reclaim", revisions, active.Revision)
	}
}

func TestDestroyingEveryReleaseOfAFunctionDeletesItsServiceWhicheverGoesFirst(t *testing.T) {
	for name, activeFirst := range map[string]bool{"the active release first": true, "the dropped release first": false} {
		t.Run(name, func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			released := newReleasedStacks(p)
			earlier := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
			later := functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two")
			released.provision(t, earlier)
			active := released.provision(t, later)
			if err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
				t.Fatalf("Pin(%s) = %v", active.Revision, err)
			}

			order := []provider.StackRef{earlier.Ref, later.Ref}
			if activeFirst {
				order = []provider.StackRef{later.Ref, earlier.Ref}
			}
			for _, ref := range order {
				released.destroy(t, ref)
			}

			if service := server.serving(); service != nil {
				t.Errorf("destroying every release left Cloud Run service %s standing", active.Physical)
			}
		})
	}
}
