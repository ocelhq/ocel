package cloudrun_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
	"github.com/ocelhq/ocel/platform/gcp/provider/cloudrun"
)

type pinRecorder struct {
	mu       sync.Mutex
	pinned   []string
	warmed   []string
	closed   map[string]bool
	untagged map[string]bool
	refuse   error
}

func (p *pinRecorder) Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	_, err := p.Pin(ctx, service, revision, stillActive)
	return err
}

func (p *pinRecorder) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) (bool, error) {
	for {
		p.mu.Lock()
		read, refused := len(p.pinned), p.refuse
		p.mu.Unlock()
		if refused != nil {
			return false, refused
		}
		if stillActive != nil {
			if err := stillActive(ctx); err != nil {
				return false, err
			}
		}
		p.mu.Lock()
		if len(p.pinned) != read {
			p.mu.Unlock()
			continue
		}
		p.pinned = append(p.pinned, service+"@"+revision)
		opened := p.closed[service]
		delete(p.closed, service)
		p.mu.Unlock()
		return opened, nil
	}
}

func (p *pinRecorder) ReadTag(_ context.Context, _, revision string) (string, error) {
	return "tag-" + revision, nil
}

func (p *pinRecorder) ReadServing(context.Context, string) (string, error) { return "", nil }

func (p *pinRecorder) Untag(_ context.Context, service, tag string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.untagged == nil {
		p.untagged = map[string]bool{}
	}
	p.untagged[service+"@"+tag] = true
	return nil
}

func (p *pinRecorder) isUntagged(service, tag string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.untagged[service+"@"+tag]
}

func (p *pinRecorder) Warm(_ context.Context, service, revision, path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.warmed = append(p.warmed, service+"@"+revision+path)
	return nil
}

func (p *pinRecorder) Close(_ context.Context, service string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed == nil {
		p.closed = map[string]bool{}
	}
	p.closed[service] = true
	return nil
}

func (p *pinRecorder) isClosed(service string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed[service]
}

func (p *pinRecorder) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.pinned)
}

const webService = "ocel-shop-prod-web"

func pinnedLast(pins *pinRecorder) string {
	calls := pins.calls()
	if len(calls) == 0 || pins.isClosed(webService) {
		return ""
	}
	return strings.TrimPrefix(calls[len(calls)-1], webService+"@rev-")
}

func refusingPins(pins *pinRecorder) func(error) {
	return func(err error) {
		pins.mu.Lock()
		defer pins.mu.Unlock()
		pins.refuse = err
	}
}

func reconciledFront(t *testing.T, pins *pinRecorder, tier environment.Tier) (*cloudrun.Edge, edge.StackState) {
	t.Helper()
	front := cloudrun.New(pins)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: tier}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	return front, stack.State()
}

type askedRevisions struct {
	mu        sync.Mutex
	revisions map[string]string
}

func (a *askedRevisions) record(move router.PointerMove) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, record := range move.Records {
		a.revisions[move.Pointer] = record.Revisions[record.Physical]
	}
}

func (a *askedRevisions) revisionOf(pointer string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.revisions[pointer]
}

type revisionRecordingRouter struct {
	cloudrun.Router
	asked *askedRevisions
}

func (r revisionRecordingRouter) Reconcile(ctx context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	stack, err := r.Router.Reconcile(ctx, spec, prior)
	return revisionRecordingStack{Stack: stack, asked: r.asked}, err
}

func (r revisionRecordingRouter) Open(state router.StackState) (router.Stack, error) {
	stack, err := r.Router.Open(state)
	return revisionRecordingStack{Stack: stack, asked: r.asked}, err
}

type revisionRecordingStack struct {
	router.Stack
	asked *askedRevisions
}

func (s revisionRecordingStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	s.asked.record(move)
	return s.Stack.MovePointer(ctx, move, progress)
}

func TestTheCloudRunRouterBehavesAsEveryRouterMust(t *testing.T) {
	routerconformance.Run(t, routerconformance.Suite{
		New: func(t *testing.T) routerconformance.Fixture {
			pins := &pinRecorder{}
			front, state := reconciledFront(t, pins, environment.TierProduction)
			return routerconformance.Fixture{
				Router:              cloudrun.NewRouter(front),
				Spec:                router.StackSpec{Tier: state.Tier, Slug: state.Slug},
				Prior:               router.NewStackState(state),
				Serving:             func(string) string { return pinnedLast(pins) },
				FailNextPointerMove: refusingPins(pins),
			}
		},
		Previews: func(t *testing.T) routerconformance.Fixture {
			pins := &pinRecorder{}
			front, state := reconciledFront(t, pins, environment.TierPreview)
			asked := &askedRevisions{revisions: map[string]string{}}
			return routerconformance.Fixture{
				Router: revisionRecordingRouter{Router: cloudrun.NewRouter(front), asked: asked},
				Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
				Prior:  router.NewStackState(state),
				Serving: func(pointer string) string {
					if _, _, deployment := router.ParseDeploymentPointer(pointer); !deployment {
						return pinnedLast(pins)
					}
					revision := asked.revisionOf(pointer)
					if revision == "" || pins.isClosed(webService) || pins.isUntagged(webService, "tag-"+revision) {
						return ""
					}
					return strings.TrimPrefix(revision, "rev-")
				},
				FailNextPointerMove: refusingPins(pins),
			}
		},
		Hostname: "shop.example.com",
		Record: func(app, build string) router.DeploymentRecord {
			return router.DeploymentRecord{App: app, Build: build, Physical: webService, Revisions: map[string]string{webService: "rev-" + build}}
		},
	})
}

func TestTheCloudRunRouterServesEachPreviewDeploymentOnItsOwnUrlAndSaysSo(t *testing.T) {
	if !cloudrun.NewRouter(cloudrun.New(&pinRecorder{})).Facts().ServesPreviewDeployments {
		t.Error("Facts() says Cloud Run serves no preview deployment of its own, and each tagged revision answers on its own run.app url")
	}
}

func previewStack(t *testing.T, pins *pinRecorder) router.Stack {
	t.Helper()
	front, state := reconciledFront(t, pins, environment.TierPreview)
	opened, err := cloudrun.NewRouter(front).Open(router.NewStackState(state))
	if err != nil {
		t.Fatalf("Open the router = %v", err)
	}
	return opened
}

func deploymentMove(pointer, promotionID, revision string) router.PointerMove {
	return router.PointerMove{
		Pointer:   router.FormatDeploymentPointer(pointer, promotionID),
		Promotion: router.Promotion{PromotionID: promotionID, Builds: map[string]string{"web": "b-" + revision}},
		Records: map[string]router.DeploymentRecord{"web": {
			App: "web", Build: "b-" + revision, Physical: webService, Revisions: map[string]string{webService: revision},
		}},
	}
}

func TestServingAPreviewDeploymentPinsNoTraffic(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := previewStack(t, pins)

	if err := stack.MovePointer(context.Background(), deploymentMove("pr-7", "p1", "web-00001-abc"), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}
	if got := pins.calls(); len(got) != 0 {
		t.Errorf("serving a preview deployment pinned %v, want nothing: the deployment answers on its revision's tag, and the preview's own url keeps what it was promoted to", got)
	}
}

func TestRemovingAPreviewDeploymentUntagsItsRevisionUnlessAnotherDeploymentStillServesIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pins := &pinRecorder{}
	stack := previewStack(t, pins)
	for _, id := range []string{"p1", "p2"} {
		if err := stack.MovePointer(ctx, deploymentMove("pr-7", id, "web-00001-abc"), progress.Discard()); err != nil {
			t.Fatalf("MovePointer(%s) = %v", id, err)
		}
	}

	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: router.FormatDeploymentPointer("pr-7", "p1")}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(p1) = %v", err)
	}
	if pins.isUntagged(webService, "tag-web-00001-abc") {
		t.Error("removing p1 untagged the revision p2 still serves on that tag")
	}
	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: router.FormatDeploymentPointer("pr-7", "p2")}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(p2) = %v", err)
	}
	if !pins.isUntagged(webService, "tag-web-00001-abc") {
		t.Error("removing the last deployment on the tag left it, and its url keeps answering")
	}
	if pins.isClosed(webService) {
		t.Error("removing a deployment closed the service, and the preview's own url stops answering")
	}
}

func fronting(t *testing.T, pins *pinRecorder) fake.PromotingStack {
	t.Helper()
	front := cloudrun.New(pins)
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{Slug: "shop", Tier: environment.TierProduction}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(shop) = %v", err)
	}
	opened, err := cloudrun.NewRouter(front).Open(router.NewStackState(stack.State()))
	if err != nil {
		t.Fatalf("Open the router = %v", err)
	}
	return fake.PromotingStack{Stack: opened, Ledger: ledger.New(fake.NewKeyValues(), environment.TierProduction, "shop")}
}

func staged(t *testing.T, stack fake.PromotingStack, identity, revision string) {
	t.Helper()
	err := stack.Ledger.PutStaged(context.Background(), router.DeploymentRecord{
		App:       "web",
		Build:     identity,
		Physical:  webService,
		Revisions: map[string]string{webService: revision},
	})
	if err != nil {
		t.Fatalf("PutStaged(%s) = %v", identity, err)
	}
}

func promoted(t *testing.T, stack fake.PromotingStack, id, identity string) error {
	t.Helper()
	return stack.MovePointer(context.Background(), router.PointerMove{Promotion: router.Promotion{
		PromotionID: id,
		Builds:      map[string]string{"web": identity},
	}}, progress.Discard())
}

func TestARollbackPinsCloudRunBackToTheRevisionThePromotionRecorded(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	staged(t, stack, "b1", "web-00001-abc")
	staged(t, stack, "b2", "web-00002-def")

	for _, step := range []struct{ id, identity string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		if err := promoted(t, stack, step.id, step.identity); err != nil {
			t.Fatalf("Promote(%s) = %v", step.id, err)
		}
	}

	want := []string{webService + "@web-00001-abc", webService + "@web-00002-def", webService + "@web-00001-abc"}
	if got := pins.calls(); !slices.Equal(got, want) {
		t.Errorf("the promotions pinned %v, want %v: a rollback that only writes the ledger leaves the newest revision serving every request", got, want)
	}
}

func TestAPromotionSaysWhichRevisionItPinsEachAppsTrafficTo(t *testing.T) {
	t.Parallel()

	stack := fronting(t, &pinRecorder{})
	staged(t, stack, "b1", "web-00001-abc")
	progress := &fake.Log{}

	promotion := router.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}}
	if err := stack.MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}
	want := "INFO Pinning all of web's traffic to revision web-00001-abc of Cloud Run service " + webService
	if got := progress.Lines(); !slices.Contains(got, want) {
		t.Errorf("the promotion said %q, want %q among it", got, want)
	}
}

func TestAPromotionWhoseRecordNamesNoRevisionIsRefusedRatherThanLeftUnpinned(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	if err := stack.Ledger.PutStaged(context.Background(), router.DeploymentRecord{
		App: "web", Build: "b1", Physical: webService,
	}); err != nil {
		t.Fatalf("PutStaged(b1) = %v", err)
	}

	var refused refusal.Refusal
	err := promoted(t, stack, "p1", "b1")
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Promote(p1) = %v, want an %s refusal: a promotion that pins nothing reports a rollback it did not perform", err, refusal.CodeInvalid)
	}
	if got := pins.calls(); len(got) != 0 {
		t.Errorf("the promotion pinned %v before refusing", got)
	}
}

func TestAPromotionThatCannotPinIsUnserved(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	staged(t, stack, "b1", "web-00001-abc")
	if err := promoted(t, stack, "p1", "b1"); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}

	staged(t, stack, "b2", "web-00002-def")
	pins.refuse = errors.New("cloud run said no")
	err := promoted(t, stack, "p2", "b2")
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("Promote(p2) = %v, want router.Unserved: nothing was pinned, so the ledger must take the promotion back", err)
	}
}

func TestAPromotionWarmsTheRevisionItPinnedOnItsOwnUrl(t *testing.T) {
	t.Parallel()

	pins := &pinRecorder{}
	stack := fronting(t, pins)
	staged(t, stack, "b1", "web-00001-abc")

	if err := promoted(t, stack, "p1", "b1"); err != nil {
		t.Fatalf("Promote(p1) = %v", err)
	}
	pins.mu.Lock()
	defer pins.mu.Unlock()
	if want := []string{webService + "@web-00001-abc/"}; !slices.Equal(pins.warmed, want) {
		t.Errorf("the promotion warmed %v, want %v: the first visitor after it pays the cold start", pins.warmed, want)
	}
}
