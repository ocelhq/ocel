package fake_test

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestARouterReportsThePropagationItsDataPlaneIsGiven(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	given := router.Propagation{Typical: 5 * time.Second}
	p.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Propagates(given)

	relay, err := p.Routers().Open(fake.RouterRelay)
	if err != nil {
		t.Fatal(err)
	}
	if got := relay.Facts().Propagation; got != given {
		t.Errorf("the relay router propagates %+v, want %+v", got, given)
	}
	direct, err := p.Routers().Open(fake.RouterDirect)
	if err != nil {
		t.Fatal(err)
	}
	if got := direct.Facts().Propagation; got == given {
		t.Errorf("the direct router propagates %+v too, want only the data plane given it to", got)
	}
}
