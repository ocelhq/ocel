package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func previewPlan(label string) providerkit.StackPlan {
	return providerkit.StackPlan{
		Ref: providerkit.StackRef{
			Project: "shop",
			Class:   providerkit.ClassPreview,
			Name:    naming.StackName{Env: "pr-7", App: "web"},
		},
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:             "web",
			Compute:         providerkit.ComputeContainer,
			Image:           "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
			HealthCheckPath: "/",
			PreviewLabel:    label,
			Functions: []providerkit.FunctionSpec{{
				Name:      "fn--web--checkout",
				Image:     "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:abc",
				Framework: providerkit.Framework{Name: "nodejs", Arch: string(providerkit.ArchX8664)},
			}},
		},
	}
}

func TestAPreviewOnTheSharedWildcardStandsUpTheServiceItsHostnameNames(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	label := edge.SharedPreview("shop", "preview.acme.com").Label("pr-7", "")

	containers, err := p.ProvisionContainers(context.Background(), previewPlan(label), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(containers) != 1 || containers[0].Physical != label {
		t.Fatalf("ProvisionContainers() stood up %+v, want the service named %q: the load balancer resolves the hostname's label "+
			"to a Cloud Run service of that name and nothing routes it anywhere else", containers, label)
	}
}

func TestAPreviewFunctionIsNamedApartFromThePreviewItShipsIn(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	label := edge.SharedPreview("shop", "preview.acme.com").Label("pr-7", "")

	functions, err := p.ProvisionFunctions(context.Background(), previewPlan(label), nil)
	if err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if len(functions) != 1 {
		t.Fatalf("ProvisionFunctions() = %+v, want the one function the plan carries", functions)
	}
	if functions[0].Physical == label {
		t.Errorf("the function is served by %q, which is the service the preview's own hostname resolves to", label)
	}
	if !strings.HasPrefix(functions[0].Physical, label) {
		t.Errorf("the function is served by %q, want it under the preview's label %q so removing the preview names it", functions[0].Physical, label)
	}
}

type heard struct {
	providerkit.Progress
	said []string
}

func (h *heard) Say(message string) { h.said = append(h.said, message) }

func (h *heard) Detail(string) {}

func TestAPreviewOnAnEdgeThatShieldsNothingIsSaidToBeOpenToAnyoneWithItsUrl(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	progress := &heard{}

	if _, err := p.ProvisionContainers(context.Background(), previewPlan(""), progress); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if !slices.ContainsFunc(progress.said, func(said string) bool { return strings.Contains(said, "is a preview and answers anyone") }) {
		t.Errorf("the release said %q, want it to say the preview answers anyone with its url: Cloud Run has no invoker a browser could satisfy, so the reader has to know", progress.said)
	}

	production := &heard{}
	plan := previewPlan("")
	plan.Ref.Class = providerkit.ClassProduction
	plan.Ref.Name = naming.StackName{Env: providerkit.ProductionEnv, App: "web"}
	if _, err := p.ProvisionContainers(context.Background(), plan, production); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if slices.ContainsFunc(production.said, func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("a production release said %q, and production is meant to answer anyone", production.said)
	}

	shielded := &heard{}
	front, err := p.Edges().Open(alb.Kind)
	if err != nil {
		t.Fatal(err)
	}
	plan = previewPlan(edge.SharedPreview("shop", "preview.acme.com").Label("pr-7", ""))
	plan.Edge = front
	if _, err := p.ProvisionContainers(context.Background(), plan, shielded); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if slices.ContainsFunc(shielded.said, func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("a preview behind the load balancer said %q, and its service takes traffic from the load balancer alone", shielded.said)
	}
}

func TestAProductionReleaseIsNamedNoDifferentlyForCarryingNoPreviewLabel(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	plan := previewPlan("")
	plan.Ref.Class = providerkit.ClassProduction
	plan.Ref.Name = naming.StackName{Env: providerkit.ProductionEnv, App: "web"}

	containers, err := p.ProvisionContainers(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	want, err := names(t, p).Service("shop", providerkit.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0].Physical != want {
		t.Errorf("ProvisionContainers() stood up %+v, want %q: production is named for the namespace, project, environment and app as it always was",
			containers, want)
	}
}

func TestAFunctionCarriesWhatTheDeployDeliveredAndWhatItsSpecNames(t *testing.T) {
	values, err := carried("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"STAGE": "one"})
	if err != nil {
		t.Fatalf("carried() = %v", err)
	}
	if values["DATABASE_URL"] != "postgres://" || values["STAGE"] != "one" {
		t.Errorf("carried() = %v, want both what the deploy resolved and what the spec named", values)
	}
}

func TestASpecThatNamesWhatTheDeployDeliveredIsRefused(t *testing.T) {
	_, err := carried("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"DATABASE_URL": "sqlite://"})
	if err == nil {
		t.Fatal("carried() let the spec take the delivered value's place, and the app would read a value nothing in it declared")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("carried() code = %v, want %v", code, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("carried() = %v, want the name that would be displaced said", err)
	}
	if !strings.Contains(err.Error(), "fn") {
		t.Errorf("carried() = %v, want what carries it said", err)
	}
}
