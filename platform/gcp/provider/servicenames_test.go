package gcp_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

var cloudRunName = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

func serviceNames(t *testing.T) gcp.Names {
	t.Helper()
	return names(t, newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}))
}

func TestAServiceIsNamedForTheProjectEnvironmentAndAppItServes(t *testing.T) {
	names := serviceNames(t)

	service, err := names.Service("shop", stackrecords.ProductionEnv, "web", "web")
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

	production, err := names.Service("shop", stackrecords.ProductionEnv, "web", "web")
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

	whole, err := names.Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	part, err := names.Service("shop", stackrecords.ProductionEnv, "web", "fn--web--checkout")
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

func TestAFunctionIsNamedByItsRouteAndNotByTheCoordinateItNames(t *testing.T) {
	names := serviceNames(t)

	service, err := names.Service("j-1874-deploy-node", stackrecords.ProductionEnv, "web", "fn--web--index")
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

func TestAServiceNameTooLongToSpellOutIsCutToLeaveItsRevisionTagRoomInTheRunAppLabel(t *testing.T) {
	names := serviceNames(t)
	const longestRunAppLabel = 46
	tag := naming.NewRelease("d1", "f1").String()

	long, err := names.Service(strings.Repeat("shopfront", 5), stackrecords.ProductionEnv, "web", "fn--web--checkout")
	if err != nil {
		t.Fatalf("Service() = %v", err)
	}
	longer, err := names.Service(strings.Repeat("shopfront", 5)+"s", stackrecords.ProductionEnv, "web", "fn--web--checkout")
	if err != nil {
		t.Fatalf("Service() = %v", err)
	}
	if len(long)+len(tag) > longestRunAppLabel {
		t.Errorf("Service() = %q (%d characters), and with the %d-character revision tag %s it is more than the %d Cloud Run takes "+
			"in the label of a tagged revision's url", long, len(long), len(tag), tag, longestRunAppLabel)
	}
	if long == longer || !cloudRunName.MatchString(long) || !strings.HasPrefix(long, "ocel-shop") || !strings.Contains(long, "-checkout-") {
		t.Errorf("Service() of projects too long to spell out = %q and %q, want the project cut before the app and its route, and each kept apart by its hash", long, longer)
	}
}

func TestAnAppNamedWithWhatCloudRunRefusesIsTurnedIntoANameItTakes(t *testing.T) {
	names := serviceNames(t)

	service, err := names.Service("Shop_Front", stackrecords.ProductionEnv, "Web API", "Web API")
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
	beside, err := names.Service("shop", stackrecords.ProductionEnv, "1-web", "1-web")
	if err != nil {
		t.Fatal(err)
	}
	if preview == beside {
		t.Errorf("both are served by %q, and a name joined by dashes alone reads two ways when every part may contain one: "+
			"a preview of one app would take another app's production service", preview)
	}
}

func TestAFunctionOfOneAppAndAnAppNamedForItAreTwoServices(t *testing.T) {
	names := serviceNames(t)

	part, err := names.Service("shop", stackrecords.ProductionEnv, "web", "fn--web--checkout")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := names.Service("shop", stackrecords.ProductionEnv, "web-checkout", "web-checkout")
	if err != nil {
		t.Fatal(err)
	}
	if part == whole {
		t.Errorf("both are served by %q, and one app's function would release over another app entirely", part)
	}
}

const previewKey edge.PreviewKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func signSharedPreviewLabel(slug, app string) string {
	return edge.NewSharedPreviewSite(slug, "preview.acme.com", previewKey).ListHosts("pr-7", "abcdefghijklmnop", []string{app})[0].ReadLabel()
}

func TestAPreviewServedOnTheSharedWildcardIsNamedTheLabelInItsHostname(t *testing.T) {
	names := serviceNames(t)
	label := signSharedPreviewLabel("shop", "web")

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

func TestAPreviewFunctionThatIsNotTheAppIsNamedApartFromThePreviewHostname(t *testing.T) {
	names := serviceNames(t)
	label := signSharedPreviewLabel("shop", "web")

	part, err := names.PreviewService(label, "web", "fn--web--checkout")
	if err != nil {
		t.Fatalf("PreviewService() = %v", err)
	}
	if !cloudRunName.MatchString(part) {
		t.Errorf("PreviewService() = %q, which Cloud Run will not take as a service name", part)
	}
	if !strings.Contains(part, "checkout") {
		t.Errorf("PreviewService() = %q, want the route the function serves named in it", part)
	}
	if part == label {
		t.Errorf("PreviewService() = %q, which is the label the preview answers on: a hostname would reach a function nothing routes to it", part)
	}
}

func TestAPreviewServiceLongerThanCloudRunTakesIsRefusedNamingTheSlugToShorten(t *testing.T) {
	names := serviceNames(t)
	slug := "the-longest-shop-anyone-named"

	_, err := names.PreviewService(signSharedPreviewLabel(slug, "web"), "web", "web")
	if err == nil {
		t.Fatal("PreviewService() named a service longer than the 49 characters Cloud Run takes")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("PreviewService() code = %v, want %v", code, refusal.CodeInvalid)
	}
	for _, part := range []string{slug, "29", "25-character token", "49"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("PreviewService() = %v, want %q named in it: the slug and its length are what a user can shorten", err, part)
		}
	}
}

func TestAPreviewFunctionServiceLongerThanCloudRunTakesIsRefusedNamingTheRoute(t *testing.T) {
	names := serviceNames(t)

	_, err := names.PreviewService(signSharedPreviewLabel("shop", "web"), "web", "fn--web--checkout-and-pay-now")
	if err == nil {
		t.Fatal("PreviewService() named a service longer than the 49 characters Cloud Run takes")
	}
	if !strings.Contains(err.Error(), "checkout-and-pay-now") {
		t.Errorf("PreviewService() = %v, want the function's route named in it: it is part of the service's name", err)
	}
}

func TestAPreviewOfAProjectWhoseSlugStartsWithADigitIsRefusedBeforeCloudRunIsAsked(t *testing.T) {
	names := serviceNames(t)

	_, err := names.PreviewService(signSharedPreviewLabel("7shop", "web"), "web", "web")
	if err == nil {
		t.Fatal("PreviewService() named a service starting with a digit, which Cloud Run will not take")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("PreviewService() code = %v, want %v", code, refusal.CodeInvalid)
	}
	for _, part := range []string{"7shop", "letter"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("PreviewService() = %v, want %q named in it: the slug is what the user can rename", err, part)
		}
	}
}

func TestAPreviewLabelNothingNamedIsRefusedRatherThanDeployingSomethingUnreachable(t *testing.T) {
	names := serviceNames(t)

	if _, err := names.PreviewService("", "web", "web"); err == nil {
		t.Fatal("PreviewService() named a service from an empty label")
	}
	if _, err := names.PreviewService("Shop-PR_7", "web", "web"); err == nil {
		t.Fatal("PreviewService() named a service from a label Cloud Run will not take")
	}
}

func TestOneAppIsNamedTheSameServiceEveryRelease(t *testing.T) {
	names := serviceNames(t)

	first, err := names.Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	again, err := names.Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Errorf("Service() named %q and then %q, and a release that renames its service strands the one already deployed", first, again)
	}
	if !cloudRunName.MatchString(first) {
		t.Errorf("Service() = %q, which Cloud Run will not take as a service name", first)
	}
}
