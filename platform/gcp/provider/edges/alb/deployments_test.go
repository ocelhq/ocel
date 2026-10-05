package alb

import (
	"context"
	"errors"
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
