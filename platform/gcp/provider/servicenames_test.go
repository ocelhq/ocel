package gcp_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
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
