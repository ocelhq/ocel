package gcp_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	if !strings.Contains(service, "-shop-prod-web-") {
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
	part, err := names.Service("shop", providerkit.ProductionEnv, "web", "fn--web--checkout")
	if err != nil {
		t.Fatal(err)
	}
	if whole == part {
		t.Errorf("both functions are served by %q, and each function on Cloud Run is a service of its own", whole)
	}
	if !strings.Contains(part, "-checkout-") {
		t.Errorf("Service() = %q, want the function it runs named in it", part)
	}
}

func TestAFunctionIsNamedByItsRouteAndNotByTheCoordinateItCarries(t *testing.T) {
	names := serviceNames(t)

	service, err := names.Service("j-1874-deploy-node", providerkit.ProductionEnv, "web", "fn--web--index")
	if err != nil {
		t.Fatalf("Service() = %v", err)
	}
	if strings.Contains(service, "fn-") {
		t.Errorf("Service() = %q, and a function's logical coordinate says fn and the app a second time: "+
			"the service is already named for the app, and the 49 characters Cloud Run builds a url from go on the route", service)
	}
	if !strings.Contains(service, "-index-") {
		t.Errorf("Service() = %q, want the route the function serves named in it", service)
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

func TestAnEnvironmentAndAnAppThatSplitTheSameLettersAreTwoServices(t *testing.T) {
	names := serviceNames(t)

	preview, err := names.Service("shop", "prod-1", "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	beside, err := names.Service("shop", providerkit.ProductionEnv, "1-web", "1-web")
	if err != nil {
		t.Fatal(err)
	}
	if preview == beside {
		t.Errorf("both are served by %q, and a name joined by dashes alone reads two ways when every part may hold one: "+
			"a preview of one app would take another app's production service", preview)
	}
}

func TestAFunctionOfOneAppAndAnAppNamedForItAreTwoServices(t *testing.T) {
	names := serviceNames(t)

	part, err := names.Service("shop", providerkit.ProductionEnv, "web", "fn--web--checkout")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := names.Service("shop", providerkit.ProductionEnv, "web-checkout", "web-checkout")
	if err != nil {
		t.Fatal(err)
	}
	if part == whole {
		t.Errorf("both are served by %q, and one app's function would release over another app entirely", part)
	}
}

func TestAPreviewServedOnTheSharedWildcardIsNamedTheLabelItsHostnameCarries(t *testing.T) {
	names := serviceNames(t)
	label := edge.SharedPreview("shop", "preview.acme.com").Label("pr-7", "")

	service, err := names.PreviewService(label, "web", "web")
	if err != nil {
		t.Fatalf("PreviewService() = %v", err)
	}
	if service != label {
		t.Errorf("PreviewService() = %q, want exactly %q: the url mask hands the whole label to Cloud Run as the service name, "+
			"so anything else answers nothing", service, label)
	}
	if !cloudRunName.MatchString(service) {
		t.Errorf("PreviewService() = %q, which Cloud Run will not take as a service name", service)
	}
}

func TestAPreviewFunctionThatIsNotTheAppIsNamedApartFromEveryPreviewHostname(t *testing.T) {
	names := serviceNames(t)
	site := edge.SharedPreview("shop", "preview.acme.com")

	part, err := names.PreviewService(site.Label("pr-7", ""), "web", "fn--web--checkout")
	if err != nil {
		t.Fatalf("PreviewService() = %v", err)
	}
	if !cloudRunName.MatchString(part) {
		t.Errorf("PreviewService() = %q, which Cloud Run will not take as a service name", part)
	}
	if !strings.Contains(part, "checkout") {
		t.Errorf("PreviewService() = %q, want the route the function serves named in it", part)
	}
	for _, app := range []string{"", "web", "checkout"} {
		if host := site.Label("pr-7", app); host == part {
			t.Errorf("PreviewService() = %q, which is the label %s answers on: a hostname would reach a function nothing routes to it", part, host)
		}
	}
	beside, err := names.PreviewService(site.Label("pr-7", "web"), "web", "fn--web--checkout")
	if err != nil {
		t.Fatalf("PreviewService() = %v", err)
	}
	if beside == part {
		t.Errorf("the one-app and the many-app preview both name %q, and they are two previews with two hostnames", part)
	}
}

func TestAPreviewLabelCloudRunWouldRefuseIsRefusedWithItsParts(t *testing.T) {
	names := serviceNames(t)
	pointer := strings.Repeat("pr-70", 12)

	_, err := names.PreviewService(edge.SharedPreview("shop", "preview.acme.com").Label(pointer, "web"), "web", "web")
	if err == nil {
		t.Fatal("PreviewService() named a service Cloud Run will not take")
	}
	if code, refused := providerkit.RefusedCode(err); !refused || code != providerkit.CodeInvalid {
		t.Errorf("PreviewService() code = %v, want %v", code, providerkit.CodeInvalid)
	}
	for _, part := range []string{"shop", pointer, "web", "60"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("PreviewService() = %v, want %q named in it: the parts and their lengths are what a user can shorten", err, part)
		}
	}
}

func TestThePreviewRefusalNamesTheDoubleDashesCloudRunTakes(t *testing.T) {
	names := serviceNames(t)
	site := edge.SharedPreview("shop", "preview.acme.com")

	if _, err := names.PreviewService(site.Label("pr-7", "web"), "web", "web"); err != nil {
		t.Fatalf("PreviewService(%q) = %v, want a label joined by %q taken", site.Label("pr-7", "web"), err, edge.PreviewAppSeparator)
	}
	_, err := names.PreviewService(site.Label(strings.Repeat("pr-70", 12), "web"), "web", "web")
	if err == nil {
		t.Fatal("PreviewService() named a service Cloud Run will not take")
	}
	if strings.Contains(err.Error(), "single dash") {
		t.Errorf("the refusal reads %v, and Cloud Run takes the %q every preview label joins its fields with", err, edge.PreviewAppSeparator)
	}
}

func TestAPreviewLabelNothingNamedIsRefusedRatherThanStandingSomethingUnreachable(t *testing.T) {
	names := serviceNames(t)

	if _, err := names.PreviewService("", "web", "web"); err == nil {
		t.Fatal("PreviewService() named a service from an empty label")
	}
	if _, err := names.PreviewService("Shop--PR_7", "web", "web"); err == nil {
		t.Fatal("PreviewService() named a service from a label Cloud Run will not take")
	}
}

func TestOneAppIsNamedTheSameServiceEveryRelease(t *testing.T) {
	names := serviceNames(t)

	first, err := names.Service("shop", providerkit.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	again, err := names.Service("shop", providerkit.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Errorf("Service() named %q and then %q, and a release that renames its service strands the one standing", first, again)
	}
	if !cloudRunName.MatchString(first) {
		t.Errorf("Service() = %q, which Cloud Run will not take as a service name", first)
	}
}
