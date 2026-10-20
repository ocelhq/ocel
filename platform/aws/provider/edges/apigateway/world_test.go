package apigateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
)

const (
	fakeStateTable  = "ocel-state"
	fakeEdgeBucket  = "ocel-prod-edge-k3x9q2"
	fakeRegion      = "eu-west-1"
	fakeAccount     = "123456789012"
	fakeInvokeRole  = "arn:aws:iam::" + fakeAccount + ":role/ocel-edge-invoke"
	fakeNotFoundAPI = "nf404"
)

type world struct {
	gateway *fakeGateway
	routing *fakeRouting
	tagging *fakeTagging
	dynamo  *fakeDynamo
	cfn     *fakeCFN
	ensured []string
}

func newWorld() *world {
	gateway := newFakeGateway()
	return &world{
		gateway: gateway,
		routing: &fakeRouting{g: gateway},
		tagging: &fakeTagging{g: gateway},
		dynamo:  newFakeDynamo(),
		cfn:     &fakeCFN{},
	}
}

func (w *world) clients() Clients {
	return Clients{APIGateway: w.gateway, Routing: w.routing, Tagging: w.tagging, Dynamo: w.dynamo, CFN: w.cfn, Region: fakeRegion}
}

func (w *world) edge() *apiGateway {
	return &apiGateway{
		ns:         defaultNamespace,
		ensurePart: w.ensurePart,
		open:       func(context.Context) (Clients, error) { return w.clients(), nil },
		delete:     w.deletion(30),
		leaseWait:  func(context.Context, time.Duration) error { return nil },
	}
}

func (w *world) ensurePart(_ context.Context, tier environment.Tier, part, addedBy, addedWith string, say func(string)) ([]provider.PartResource, error) {
	w.ensured = append(w.ensured, string(tier)+" "+part+" for "+addedBy+addedWith)
	if w.cfn.absent {
		return nil, fmt.Errorf("the %s tier is not bootstrapped: run `%s`", tier, provider.BootstrapCommand(tier))
	}
	if say != nil {
		say("added the " + part + " part")
	}
	return []provider.PartResource{{Kind: notfoundapi.KindRestAPI, ID: fakeNotFoundAPI}}, nil
}

func (w *world) deletion(attempts int) *Deletion {
	return &Deletion{
		Wait: func(_ context.Context, interval time.Duration) error {
			w.gateway.note("wait " + interval.String())
			return nil
		},
		Attempts: attempts,
		Every:    30 * time.Second,
		Jitter:   func() float64 { return 0 },
	}
}

func ownState(t *testing.T, stack edge.EdgeStack) private {
	t.Helper()

	var own private
	if err := stack.State().Private.Into(&own); err != nil {
		t.Fatalf("read the state the edge keeps to itself: %v", err)
	}
	return own
}

func openRouter(shared edge.EdgeStack) fake.PromotingStack {
	s := shared.(*stack)
	c, err := s.p.clientsFor(context.Background())
	if err != nil {
		panic(err)
	}
	return fake.PromotingStack{Stack: routerStack{s: s}, Ledger: s.openLedger(c)}
}

func removePointer(ctx context.Context, stack edge.EdgeStack, removal router.PointerRemoval, progress progress.Log) (router.PruneResult, error) {
	if err := openRouter(stack).RemovePointer(ctx, removal, progress); err != nil {
		return router.PruneResult{}, err
	}
	return openRouter(stack).Ledger.RemovePointer(ctx, removal.Pointer)
}
