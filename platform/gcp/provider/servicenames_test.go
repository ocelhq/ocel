package gcp_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var cloudRunName = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

func serviceNames(t *testing.T) gcp.Names {
	t.Helper()
	return newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Names()
}

func TestAServiceIsNamedForTheProjectEnvironmentAndAppItServes(t *testing.T) {
	names := serviceNames(t)

	service, err := names.Service("shop", providerkit.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatalf("Service() = %v", err)
	}
	if !strings.HasSuffix(service, "-shop-prod-web") {
		t.Errorf("Service() = %q, want the project, the environment and the app in the name a url is built from", service)
	}
	if !strings.HasPrefix(service, names.Namespace().String()+"-") {
		t.Errorf("Service() = %q, want it under the namespace, which is what keeps two installations in one project apart", service)
	}
	if !cloudRunName.MatchString(service) {
		t.Errorf("Service() = %q, which Cloud Run will not take as a service name", service)
	}
}

func TestTwoEnvironmentsOfOneAppAreTwoServices(t *testing.T) {
	names := serviceNames(t)

	production, err := names.Service("shop", providerkit.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := names.Service("shop", "pr-7", "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if production == preview {
		t.Errorf("both environments are served by %q, and a preview would take production's place", production)
	}
}

func TestAFunctionThatIsNotTheAppItselfIsNamedApart(t *testing.T) {
	names := serviceNames(t)

	whole, err := names.Service("shop", providerkit.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	part, err := names.Service("shop", providerkit.ProductionEnv, "web", "web-checkout")
	if err != nil {
		t.Fatal(err)
	}
	if whole == part {
		t.Errorf("both functions are served by %q, and each function on Cloud Run is a service of its own", whole)
	}
	if !strings.HasSuffix(part, "-checkout") {
		t.Errorf("Service() = %q, want the function it runs named in it", part)
	}
}

func TestANameCloudRunWouldNotBuildAUrlFromIsRefused(t *testing.T) {
	names := serviceNames(t)

	_, err := names.Service(strings.Repeat("shopfront", 5), providerkit.ProductionEnv, "web", "web")
	if err == nil {
		t.Fatal("Service() named a service too long for Cloud Run to build a url from")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("Service() code = %v, want %v", code, providerkit.CodeInvalid)
	}
	if !strings.Contains(err.Error(), providerkit.NamespaceEnvVar) {
		t.Errorf("Service() = %v, want it to say what a user can shorten", err)
	}
}

func TestAnAppNamedWithWhatCloudRunRefusesIsCarriedIntoANameItTakes(t *testing.T) {
	names := serviceNames(t)

	service, err := names.Service("Shop_Front", providerkit.ProductionEnv, "Web API", "Web API")
	if err != nil {
		t.Fatalf("Service() = %v", err)
	}
	if !cloudRunName.MatchString(service) {
		t.Errorf("Service() = %q, which Cloud Run will not take as a service name", service)
	}
}
