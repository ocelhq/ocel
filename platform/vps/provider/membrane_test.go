package vps_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func refusingReach(t *testing.T, resources []providerkit.Resource, grants []providerkit.Link) error {
	t.Helper()
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "box.example", User: "ocel-deploy"}})
	return providerkit.RefuseUnreachableLinks(p.Vendor(), p.Serves(), providerkit.CrossesMembrane, resources, grants)
}

func TestABoxRefusesALinkItServesNoMembraneFor(t *testing.T) {
	t.Parallel()

	grants := []providerkit.Link{{Name: "uploads", Resource: "bucket--uploads", Type: providerkit.LinkBucket}}
	err := refusingReach(t, nil, grants)

	var unreachable *providerkit.UnreachableLinkError
	if !errors.As(err, &unreachable) {
		t.Fatalf("a bucket link consumed on a box = %v, want it refused before the app is handed a record it cannot read", err)
	}
	for _, want := range []string{"bucket--uploads", string(providerkit.LinkBucket), string(vps.Vendor)} {
		if !strings.Contains(unreachable.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", unreachable.Error(), want)
		}
	}
	if strings.Contains(strings.ToLower(unreachable.Error()), "aws") {
		t.Errorf("a box's refusal reads %q and names another vendor's provider", unreachable.Error())
	}
}

func TestABoxIsLetPastForALinkTypeThatReachesItsProviderDirectly(t *testing.T) {
	t.Parallel()

	resources := []providerkit.Resource{{Name: "database--main", Declared: "database--main", Type: providerkit.LinkPostgres}}
	if err := refusingReach(t, resources, nil); err != nil {
		t.Fatalf("a postgres record on a box = %v, want nothing refused: postgres reaches its provider without a membrane", err)
	}
}
