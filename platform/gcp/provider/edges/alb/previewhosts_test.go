package alb

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

func TestRemovingADeploymentTakesTheTagOffEveryServiceItsDeployTagged(t *testing.T) {
	t.Parallel()

	const fnService = "ocel-shop-prod-fn--web--api"
	ctx := context.Background()
	w, stack := previewRouter(t)
	record := previewRecord("b1")
	record.Revisions[fnService] = fnService + "-b1"
	move := deploymentMove(deploymentFirst, deploymentHost, record)
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}
	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: move.Pointer, Hosts: move.Hosts}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(deployment) = %v", err)
	}
	got := w.untagged()
	for _, service := range []string{previewService, fnService} {
		want := service + "#" + w.tagOf(service, record.Revisions[service])
		if !slices.Contains(got, want) {
			t.Errorf("the removal untagged %v, want it to include %s", got, want)
		}
	}
}

func TestAHostnameAPromotionTakesFromADeploymentLeavesItsTagToBeUntaggedWhenTheDeploymentGoes(t *testing.T) {
	t.Parallel()

	const retaken = "shop-dddddddddddddddd.preview.example.com"
	ctx := context.Background()
	w, stack := previewRouter(t)
	record := previewRecord("b1")
	move := deploymentMove(deploymentFirst, deploymentHost, record)
	move.Hosts = append(move.Hosts, edge.PreviewHost{Hostname: retaken, App: record.App})
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}
	if err := stack.MovePointer(ctx, aliasMove("p2", retaken, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(alias) = %v", err)
	}
	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: move.Pointer, Hosts: move.Hosts[:1]}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(deployment) = %v", err)
	}

	tag := w.tagOf(previewService, record.Revisions[previewService])
	if got := w.untagged(); !slices.Contains(got, previewService+"#"+tag) {
		t.Errorf("the removal untagged %v, want %s#%s: %s now serves the alias, so no hostname holds the deployment's tag", got, previewService, tag, retaken)
	}
}

func TestADeploymentsOnlyHostnameAPromotionTakesTakesTheDeploymentsTagOffItsRevision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	record := previewRecord("b1")
	move := deploymentMove(deploymentFirst, deploymentHost, record)
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}
	if err := stack.MovePointer(ctx, aliasMove("p2", deploymentHost, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(alias) = %v", err)
	}

	tag := w.tagOf(previewService, record.Revisions[previewService])
	if got := w.untagged(); !slices.Equal(got, []string{previewService + "#" + tag}) {
		t.Errorf("the re-take untagged %v, want %s#%s once: the alias reaches the service, so no hostname holds the deployment's tag", got, previewService, tag)
	}
	if err := stack.RemovePointer(ctx, router.PointerRemoval{Pointer: move.Pointer, Hosts: move.Hosts}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(deployment) = %v", err)
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

func TestAnAliasRotatedOntoAFullUrlMapIsRoutedInPlaceOfTheOneItSupersedes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.fillHostRules(tierRoutes, maxHostRules)
	rotated := aliasMove("p2", rotatedAlias, previewRecord("b2"))
	rotated.Superseded = []edge.PreviewHost{{Hostname: aliasHost, App: "web"}}
	if err := stack.MovePointer(ctx, rotated, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p2) onto a full url map = %v, want it routed: the alias it supersedes frees its host rule", err)
	}

	routed := w.hosts(tierRoutes)
	if _, still := routed[aliasHost]; still {
		t.Errorf("the tier url map routes %s, want it gone", aliasHost)
	}
	if routed[rotatedAlias] == "" {
		t.Errorf("the tier url map routes no %s, want it routed", rotatedAlias)
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

const siblingAlias = "shop-dddddddddddddddd.preview.example.com"

func rotatedMove(promotionID string, record router.DeploymentRecord, superseded ...string) router.PointerMove {
	move := aliasMove(promotionID, rotatedAlias, record)
	for _, hostname := range superseded {
		move.Superseded = append(move.Superseded, edge.PreviewHost{Hostname: hostname, App: "web"})
	}
	return move
}

func wantAliasRouted(t *testing.T, w *world, hostname string) {
	t.Helper()
	if got, want := w.hosts(tierRoutes)[hostname], backendName("shop", environment.TierPreview, hostname); got != want {
		t.Errorf("the tier url map routes %s to %q, want %q: the move failed, so it keeps serving", hostname, got, want)
	}
}

func TestAnAliasWhoseReplacementFailsToRaiseIsRoutedAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.breakUp(BindingStack("shop", environment.TierPreview), errors.New("the binding stack would not rise"))
	err := stack.MovePointer(ctx, rotatedMove("p2", previewRecord("b2"), aliasHost), progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want it unserved", err)
	}
	wantAliasRouted(t, w, aliasHost)
	if got, routed := w.hosts(tierRoutes)[rotatedAlias]; routed {
		t.Errorf("%s is routed to %q, want nothing", rotatedAlias, got)
	}
}

func TestAnAliasWhoseReplacementFailsToRouteIsRoutedAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.refuseRoute(rotatedAlias, errors.New("compute refused the host rule"))
	err := stack.MovePointer(ctx, rotatedMove("p2", previewRecord("b2"), aliasHost), progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want it unserved", err)
	}
	wantAliasRouted(t, w, aliasHost)
	if got, routed := w.hosts(tierRoutes)[rotatedAlias]; routed {
		t.Errorf("%s is routed to %q, want nothing", rotatedAlias, got)
	}
}

func TestAnAliasRotatedOntoAFullUrlMapThatFailsToRouteGetsItsHostRuleBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.fillHostRules(tierRoutes, maxHostRules)
	w.refuseRoute(rotatedAlias, errors.New("compute refused the host rule"))
	err := stack.MovePointer(ctx, rotatedMove("p2", previewRecord("b2"), aliasHost), progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want it unserved", err)
	}
	wantAliasRouted(t, w, aliasHost)
	if got := len(w.hosts(tierRoutes)); got != maxHostRules {
		t.Errorf("the tier url map holds %d host rules, want %d", got, maxHostRules)
	}
}

func TestASupersededAliasThatFailsToUnrouteLeavesTheAliasesBeforeItRouted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	first := aliasMove("p1", aliasHost, previewRecord("b1"))
	first.Hosts = append(first.Hosts, edge.PreviewHost{Hostname: siblingAlias, App: "web"})
	if err := stack.MovePointer(ctx, first, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.refuseUnroute(siblingAlias, errors.New("compute refused to drop the host rule"))
	err := stack.MovePointer(ctx, rotatedMove("p2", previewRecord("b2"), aliasHost, siblingAlias), progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want it unserved", err)
	}
	wantAliasRouted(t, w, aliasHost)
	wantAliasRouted(t, w, siblingAlias)
}

func TestASupersededAliasIsRoutedAgainWhenItsMoveFailsToPin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	if err := stack.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.refusePins(errors.New("cloud run would not pin the revision"))
	err := stack.MovePointer(ctx, rotatedMove("p2", previewRecord("b2"), aliasHost), progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want it unserved", err)
	}
	wantAliasRouted(t, w, aliasHost)
	if got, routed := w.hosts(tierRoutes)[rotatedAlias]; routed {
		t.Errorf("%s is routed to %q, want nothing", rotatedAlias, got)
	}
	if _, recorded := stack.s.recorded.Hosts[aliasHost]; !recorded {
		t.Errorf("the router records %v, want %s still recorded", stack.s.recorded.Hosts, aliasHost)
	}
	if _, recorded := stack.s.recorded.Hosts[rotatedAlias]; recorded {
		t.Errorf("the router records %v, want %s not recorded", stack.s.recorded.Hosts, rotatedAlias)
	}
}

func TestASupersededAliasWhoseUnrouteAppliedBeforeItFailedIsRoutedAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack := previewRouter(t)
	first := aliasMove("p1", aliasHost, previewRecord("b1"))
	first.Hosts = append(first.Hosts, edge.PreviewHost{Hostname: siblingAlias, App: "web"})
	if err := stack.MovePointer(ctx, first, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(p1) = %v", err)
	}
	w.refuseUnrouteAfterApplying(siblingAlias, errors.New("compute dropped the host rule and then lost the reply"))
	err := stack.MovePointer(ctx, rotatedMove("p2", previewRecord("b2"), aliasHost, siblingAlias), progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer(p2) = %v, want it unserved", err)
	}
	wantAliasRouted(t, w, aliasHost)
	wantAliasRouted(t, w, siblingAlias)
}

var errUntagRefused = errors.New("cloud run refused to remove the tag")

func owingATag(t *testing.T) (*world, routerStack, router.DeploymentRecord, router.PointerMove, string) {
	t.Helper()
	ctx := context.Background()
	w, stack := previewRouter(t)
	record := previewRecord("b1")
	move := deploymentMove(deploymentFirst, deploymentHost, record)
	if err := stack.MovePointer(ctx, move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(deployment) = %v", err)
	}
	w.refuseUntags(errUntagRefused)
	return w, stack, record, move, w.tagOf(previewService, record.Revisions[previewService])
}

func TestATagWhoseUntagFailedIsTakenOffByTheNextMove(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack, record, _, tag := owingATag(t)
	log := &warnings{Log: progress.Discard()}
	if err := stack.MovePointer(ctx, aliasMove("p2", deploymentHost, record), log); err != nil {
		t.Fatalf("MovePointer(alias) = %v, want nil: traffic moved, so the deploy did not fail", err)
	}
	if len(log.said) != 1 || !strings.Contains(log.said[0], tag) {
		t.Errorf("the move warned %v, want one warning naming the tag %s it could not remove", log.said, tag)
	}
	if got, want := w.pins(), []string{previewService + "@" + record.Revisions[previewService]}; !slices.Equal(got, want) {
		t.Errorf("the alias move pinned %v, want %v: a failed untag must not stop traffic moving", got, want)
	}
	if got, want := stack.s.recorded.TagsToRemove, []RevisionTag{{Service: previewService, Tag: tag}}; !slices.Equal(got, want) {
		t.Errorf("the router records %v owed removal, want %v", got, want)
	}

	w.refuseUntags(nil)
	if err := stack.MovePointer(ctx, aliasMove("p2", deploymentHost, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(alias) again = %v", err)
	}
	if got, want := w.untagged(), []string{previewService + "#" + tag}; !slices.Equal(got, want) {
		t.Errorf("the retry untagged %v, want %v exactly once", got, want)
	}
	if got := stack.s.recorded.TagsToRemove; got != nil {
		t.Errorf("the router still records %v owed removal", got)
	}
}

func TestATagWhoseUntagFailedIsTakenOffWhenItsDeploymentIsRemovedAgain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w, stack, _, move, tag := owingATag(t)
	removal := router.PointerRemoval{Pointer: move.Pointer, Hosts: move.Hosts}
	if err := stack.RemovePointer(ctx, removal, progress.Discard()); !errors.Is(err, errUntagRefused) {
		t.Fatalf("RemovePointer(deployment) = %v, want the untag error", err)
	}

	w.refuseUntags(nil)
	if err := stack.RemovePointer(ctx, removal, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(deployment) again = %v", err)
	}
	if got, want := w.untagged(), []string{previewService + "#" + tag}; !slices.Equal(got, want) {
		t.Errorf("the removals untagged %v, want %v once", got, want)
	}
	if got := stack.s.recorded.TagsToRemove; len(got) != 0 {
		t.Errorf("the router still records %v owed removal", got)
	}
}

func TestATagStillOwedRemovalThatAHostnameHoldsAgainStaysOnItsRevision(t *testing.T) {
	t.Parallel()

	const second = "shop-eeeeeeeeeeeeeeee.preview.example.com"
	ctx := context.Background()
	w, stack, record, _, tag := owingATag(t)
	if err := stack.MovePointer(ctx, aliasMove("p2", deploymentHost, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(alias) = %v, want nil", err)
	}

	w.refuseUntags(nil)
	if err := stack.MovePointer(ctx, deploymentMove(deploymentFirst, second, record), progress.Discard()); err != nil {
		t.Fatalf("MovePointer(second host) = %v", err)
	}
	if slices.Contains(w.untagged(), previewService+"#"+tag) {
		t.Errorf("the router untagged %s#%s while %s holds it", previewService, tag, second)
	}
	if got := stack.s.recorded.TagsToRemove; len(got) != 0 {
		t.Errorf("the router still records %v owed removal", got)
	}
}
