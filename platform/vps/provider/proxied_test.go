package vps_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func refusingReach(t *testing.T, resources []providerkit.Resource, grants []providerkit.Binding) error {
	t.Helper()
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "box.example", User: "ocel-deploy"}})
	return providerkit.RefuseUnreachableBindings(p.Vendor(), p.Serves(), providerkit.Proxied, resources, grants)
}

func TestABoxRefusesAProxiedBindingItServesNothingFor(t *testing.T) {
	t.Parallel()

	grants := []providerkit.Binding{{Name: "uploads", Resource: "bucket--uploads", Type: providerkit.BindingBucket}}
	err := refusingReach(t, nil, grants)

	var unreachable *providerkit.UnreachableBindingError
	if !errors.As(err, &unreachable) {
		t.Fatalf("a bucket binding consumed on a box = %v, want it refused before the app is handed a record it cannot read", err)
	}
	for _, want := range []string{"bucket--uploads", string(providerkit.BindingBucket), string(vps.Vendor)} {
		if !strings.Contains(unreachable.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", unreachable.Error(), want)
		}
	}
	if strings.Contains(strings.ToLower(unreachable.Error()), "aws") {
		t.Errorf("a box's refusal reads %q and names another vendor's provider", unreachable.Error())
	}
}

func TestABoxIsLetPastForABindingTypeThatReachesItsProviderDirectly(t *testing.T) {
	t.Parallel()

	resources := []providerkit.Resource{{Name: "database--main", Declared: "database--main", Type: providerkit.BindingPostgres}}
	if err := refusingReach(t, resources, nil); err != nil {
		t.Fatalf("a postgres record on a box = %v, want nothing refused: postgres reaches its provider without a runtime", err)
	}
}
