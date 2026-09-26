package vps_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func refusingReach(t *testing.T, resources []provider.Resource, grants []provider.Binding) error {
	t.Helper()
	proxied := func(kind provider.BindingType) bool {
		return naming.Proxied(provider.WireBindingType(kind)) || kind == provider.BindingType("queue")
	}
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "box.example", User: "ocel-deploy"}})
	return providerserver.RefuseUnreachableBindings(p.Facts().Vendor, p.Facts().Bindings, proxied, resources, grants)
}

func TestABoxRefusesAProxiedBindingItServesNothingFor(t *testing.T) {
	t.Parallel()

	grants := []provider.Binding{{Name: "queue", Resource: "queue--jobs", Type: provider.BindingType("queue")}}
	err := refusingReach(t, nil, grants)

	var unreachable *providerserver.UnreachableBindingError
	if !errors.As(err, &unreachable) {
		t.Fatalf("a proxied binding a box serves nothing for = %v, want it refused before the app is handed a record it cannot read", err)
	}
	for _, want := range []string{"queue--jobs", "queue", string(vps.Vendor)} {
		if !strings.Contains(unreachable.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", unreachable.Error(), want)
		}
	}
	if strings.Contains(strings.ToLower(unreachable.Error()), "aws") {
		t.Errorf("a box's refusal reads %q and names another vendor's provider", unreachable.Error())
	}
}

func TestABoxIsLetPastForTheBucketsItNowServesItself(t *testing.T) {
	t.Parallel()

	grants := []provider.Binding{{Name: "uploads", Resource: "bucket--uploads", Type: provider.BindingBucket}}
	if err := refusingReach(t, nil, grants); err != nil {
		t.Fatalf("a bucket binding consumed on a box = %v, want nothing refused: a box stands a store up for it", err)
	}
}

func TestABoxIsLetPastForABindingTypeThatReachesItsProviderDirectly(t *testing.T) {
	t.Parallel()

	resources := []provider.Resource{{Name: "database--main", Declared: "database--main", Type: provider.BindingPostgres}}
	if err := refusingReach(t, resources, nil); err != nil {
		t.Fatalf("a postgres record on a box = %v, want nothing refused: postgres reaches its provider without a runtime", err)
	}
}
