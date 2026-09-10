package gcp

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
				Name:    "fn--web--checkout",
				Image:   "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:abc",
				Runtime: providerkit.Runtime{Name: "nodejs", Arch: string(providerkit.ArchX8664)},
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
	want, err := p.Names().Service("shop", providerkit.ProductionEnv, "web", "web")
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
