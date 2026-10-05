package alb

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	previewService  = "ocel-shop-pr-7-web"
	deploymentHost  = "shop-aaaaaaaaaaaaaaaa.preview.example.com"
	tierRoutes      = "ocel-alb-production-routes"
	deploymentFirst = "p1"
)

var errDropped = errors.New("a prune dropped this promotion")

func previewRecord(build string) router.DeploymentRecord {
	return router.DeploymentRecord{
		App: "web", Build: build, Physical: previewService,
		Revisions: map[string]string{previewService: previewService + "-" + build},
	}
}

func deploymentMove(promotionID, hostname string, record router.DeploymentRecord) router.PointerMove {
	return router.PointerMove{
		Pointer:   router.FormatDeploymentPointer("pr-7", promotionID),
		Promotion: router.Promotion{PromotionID: promotionID, Builds: map[string]string{record.App: record.Build}},
		Records:   map[string]router.DeploymentRecord{record.App: record},
		Hosts:     []edge.PreviewHost{{Hostname: hostname, App: record.App}},
	}
}

func previewRouter(t *testing.T) (*world, routerStack) {
	t.Helper()
	_, w, shared := reconciledPreview(t)
	return w, routerStack{s: shared.(*stack)}
}

func TestADeploymentHostnameIsRoutedToTheRevisionItsDeployTagged(t *testing.T) {
	t.Parallel()

	w, stack := previewRouter(t)
	record := previewRecord("b1")
	if err := stack.MovePointer(context.Background(), deploymentMove(deploymentFirst, deploymentHost, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}

	backend := backendName("shop", environment.TierPreview, deploymentHost)
	if got := w.hosts(tierRoutes)[deploymentHost]; got != backend {
		t.Errorf("%s is routed to %q, want its own backend %q: a deployment hostname is an exact host rule", deploymentHost, got, backend)
	}
	neg, declaredNEG := w.declarations(BindingStack("shop", environment.TierPreview))[negName("shop", environment.TierPreview, deploymentHost)]
	if !declaredNEG {
		t.Fatalf("the binding declares no serverless neg for %s", deploymentHost)
	}
	run, _ := neg.Args["cloudRun"].(map[string]any)
	if run["service"] != previewService || run["tag"] != w.tagOf(previewService, record.Revisions[previewService]) {
		t.Errorf("the neg points at %v, want service %s and the tag on revision %s: the tag is what pins a deployment hostname to the revision its deploy created",
			run, previewService, record.Revisions[previewService])
	}
	if pinned := w.pins(); len(pinned) != 0 {
		t.Errorf("serving a deployment hostname pinned %v, want nothing pinned: a deployment is reached through its tag and moves no traffic", pinned)
	}
}

func TestADeploymentWhosePromotionWasDroppedRoutesNothing(t *testing.T) {
	t.Parallel()

	w, stack := previewRouter(t)
	move := deploymentMove(deploymentFirst, deploymentHost, previewRecord("b1"))
	move.StillActive = func(context.Context) error { return errDropped }
	err := stack.MovePointer(context.Background(), move, progress.Discard())
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(deployment) whose promotion was dropped = %v, want it unserved", err)
	}
	if got, routed := w.hosts(tierRoutes)[deploymentHost]; routed {
		t.Errorf("%s is routed to %q, want nothing: the deployment it names is no longer kept", deploymentHost, got)
	}
}

func TestADeploymentHostnameTheUrlMapHasNoHostRuleLeftForIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	t.Parallel()

	w, stack := previewRouter(t)
	w.fillHostRules(tierRoutes, maxHostRules)
	err := stack.MovePointer(context.Background(), deploymentMove(deploymentFirst, deploymentHost, previewRecord("b1")), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("MovePointer onto a url map with %d host rules = %v, want a refusal", maxHostRules, err)
	}
	if _, declared := w.declarations(BindingStack("shop", environment.TierPreview))[negName("shop", environment.TierPreview, deploymentHost)]; declared {
		t.Errorf("the binding declares a neg for %s, want nothing raised: Compute refuses the host rule only after the neg and backend service exist", deploymentHost)
	}
}

func TestADeploymentHostnameTheProjectHasNoBackendServiceQuotaLeftForIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	t.Parallel()

	w, stack := previewRouter(t)
	w.setBackendServiceQuota(BackendServiceQuota{Usage: 50, Limit: 50})
	err := stack.MovePointer(context.Background(), deploymentMove(deploymentFirst, deploymentHost, previewRecord("b1")), progress.Discard())
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("MovePointer with every backend service the project's quota allows in use = %v, want a refusal", err)
	}
	if _, declared := w.declarations(BindingStack("shop", environment.TierPreview))[negName("shop", environment.TierPreview, deploymentHost)]; declared {
		t.Errorf("the binding declares a neg for %s, want nothing raised: Compute refuses the backend service only after the neg exists", deploymentHost)
	}
}

func TestADeploymentHostnameIsRoutedOntoTheLastBackendServiceTheProjectQuotaAllows(t *testing.T) {
	t.Parallel()

	w, stack := previewRouter(t)
	w.setBackendServiceQuota(BackendServiceQuota{Usage: 49, Limit: 50})
	if err := stack.MovePointer(context.Background(), deploymentMove(deploymentFirst, deploymentHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer with one backend service of quota left = %v", err)
	}
	if _, routed := w.hosts(tierRoutes)[deploymentHost]; !routed {
		t.Errorf("%s is not routed, want it routed onto the last backend service the quota allows", deploymentHost)
	}
}

func TestRemovingADeploymentUnroutesItsHostnameAndTakesTheTagOffItsRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	record := previewRecord("b1")
	move := deploymentMove(deploymentFirst, deploymentHost, record)
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}

	removal := router.PointerRemoval{Pointer: move.Pointer, Hosts: move.Hosts}
	for range 2 {
		if err := stack.RemovePointer(ctx, removal, progress.Discard()); err != nil {
			t.Fatalf("RemovePointer(deployment) = %v", err)
		}
	}

	if got, routed := w.hosts(tierRoutes)[deploymentHost]; routed {
		t.Errorf("%s is still routed to %q after its deployment was removed", deploymentHost, got)
	}
	if got := w.torn(); !slices.Contains(got, BindingStack("shop", environment.TierPreview)) {
		t.Errorf("the router tore down %v, want the project's binding stack: its last backend served the removed deployment", got)
	}
	tag := w.tagOf(previewService, record.Revisions[previewService])
	if got := w.untagged(); !slices.Equal(got, []string{previewService + "#" + tag}) {
		t.Errorf("the removal untagged %v, want %s#%s once: a tag no hostname reaches keeps a revision prune would otherwise delete", got, previewService, tag)
	}
	if _, recorded := stack.s.recorded.Hosts[deploymentHost]; recorded {
		t.Errorf("the router still records %s after its deployment was removed", deploymentHost)
	}
}

const (
	aliasHost      = "shop-bbbbbbbbbbbbbbbb.preview.example.com"
	rotatedAlias   = "shop-cccccccccccccccc.preview.example.com"
	previewPointer = "pr-7"
)

func aliasMove(promotionID, hostname string, record router.DeploymentRecord) router.PointerMove {
	move := deploymentMove(promotionID, hostname, record)
	move.Pointer = previewPointer
	return move
}

func TestAnAliasTheNextPromotionNoLongerNamesIsUnrouted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	rotated := aliasMove("p2", rotatedAlias, previewRecord("b2"))
	rotated.Superseded = []edge.PreviewHost{{Hostname: aliasHost, App: "web"}}
	if err := stack.MovePointer(ctx, rotated, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p2) = %v", err)
	}

	routed := w.hosts(tierRoutes)
	if _, still := routed[aliasHost]; still {
		t.Errorf("the tier url map routes %v, want %s gone: the promotion that rotated the alias superseded it", routed, aliasHost)
	}
	if routed[rotatedAlias] == "" {
		t.Errorf("the tier url map routes %v, want %s routed", routed, rotatedAlias)
	}
}

func TestRemovingAPreviewPointerUnroutesItsAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	move := aliasMove("p1", aliasHost, previewRecord("b1"))
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: previewPointer, Hosts: move.Hosts}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer = %v", err)
	}

	if routed := w.hosts(tierRoutes); len(routed) != 0 {
		t.Errorf("the tier url map routes %v once the preview was removed, want nothing", routed)
	}
	if got := w.untagged(); len(got) != 0 {
		t.Errorf("removing the alias untagged %v, want nothing: an alias reaches its service, not a tag", got)
	}
}

type movedHosts struct {
	mu    sync.Mutex
	hosts map[string][]string
}

func (m *movedHosts) record(move router.PointerMove) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(move.Hosts) > 0 {
		m.hosts[move.Pointer] = edge.ListPreviewHostnames(move.Hosts)
	}
}

func (m *movedHosts) listHostsOf(pointer string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hosts[pointer]
}

type hostRecordingRouter struct {
	Router
	moved *movedHosts
}

func (r hostRecordingRouter) Reconcile(ctx context.Context, spec router.StackSpec, prior router.StackState) (router.Stack, error) {
	stack, err := r.Router.Reconcile(ctx, spec, prior)
	return hostRecordingStack{Stack: stack, moved: r.moved}, err
}

func (r hostRecordingRouter) Open(state router.StackState) (router.Stack, error) {
	stack, err := r.Router.Open(state)
	return hostRecordingStack{Stack: stack, moved: r.moved}, err
}

type hostRecordingStack struct {
	router.Stack
	moved *movedHosts
}

func (s hostRecordingStack) MovePointer(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	s.moved.record(move)
	return s.Stack.MovePointer(ctx, move, progress)
}

func TestARouterThatServesEachPreviewDeploymentOnItsOwnHostnameSaysSo(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	if !NewRouter(balancer).Facts().ServesPreviewDeployments {
		t.Error("Facts() says the alb router serves no preview deployment on its own hostname, so a deploy would never announce one")
	}
}
