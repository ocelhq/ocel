package pin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type services struct {
	serving     map[string]string
	refuse      map[string]error
	failAfter   map[string]error
	checkActive bool
}

func (s *services) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	if err := s.refuse[service]; err != nil {
		return err
	}
	if s.checkActive && stillActive != nil {
		if err := stillActive(ctx); err != nil {
			return err
		}
	}
	s.serving[service] = revision
	if err := s.failAfter[service]; err != nil {
		delete(s.failAfter, service)
		return err
	}
	return nil
}

func (s *services) ReadServing(_ context.Context, service string) (string, error) {
	return s.serving[service], nil
}

func (s *services) ReadTag(context.Context, string, string) (string, error) { return "", nil }

func (s *services) Untag(context.Context, string, string) error { return nil }

func promotion(builds map[string]string) router.PointerMove {
	move := router.PointerMove{
		Promotion: router.Promotion{PromotionID: "p2", Builds: map[string]string{}},
		Records:   map[string]router.DeploymentRecord{},
	}
	for app, revision := range builds {
		service := "ocel-shop-prod-" + app
		move.Promotion.Builds[app] = "b2"
		move.Records[app] = router.DeploymentRecord{App: app, Build: "b2", Physical: service, Revisions: map[string]string{service: revision}}
	}
	return move
}

func TestAPromotionThatFailsPartWayPutsEveryServiceItPinnedBackOnWhatItServed(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}

	err := pin.MovePointer(context.Background(), cloudRun, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, refused) {
		t.Fatalf("MovePointer = %v, want the refusal that stopped it", err)
	}
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Errorf("MovePointer = %v, want it unserved: every service is back on what it served, so the ledger must take the promotion back", err)
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-1" {
		t.Errorf("api serves %s after the promotion failed on web, want the api-1 it served before: one promotion is all of its apps or none", got)
	}
}

func TestAPromotionThatCannotPutAServiceBackSaysWhatEachServiceServes(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}
	flaky := &restoreRefusing{services: cloudRun, refuseAfter: 1}

	err := pin.MovePointer(context.Background(), flaky, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	var unserved router.Unserved
	if errors.As(err, &unserved) {
		t.Fatalf("MovePointer = %v, want it served: api still serves api-2, so the ledger must keep naming the promotion that moved it", err)
	}
	if err == nil || !strings.Contains(err.Error(), "ocel-shop-prod-api") || !strings.Contains(err.Error(), "api-2") {
		t.Errorf("MovePointer = %v, want it to say ocel-shop-prod-api is left serving api-2", err)
	}
}

type restoreRefusing struct {
	*services
	refuseAfter int
	pins        int
}

func (r *restoreRefusing) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	r.pins++
	if r.pins > r.refuseAfter+1 {
		return errors.New("cloud run refused the restore")
	}
	return r.services.Pin(ctx, service, revision, stillActive)
}

func TestAPromotionDisplacedPartWayLeavesWhatTheDisplacingPromotionPinned(t *testing.T) {
	cloudRun := &services{serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"}, checkActive: true}
	displaced := errors.New("another promotion displaced this one")
	move := promotion(map[string]string{"api": "api-2", "web": "web-2"})
	checks := 0
	move.StillActive = func(context.Context) error {
		checks++
		if checks < 3 {
			return nil
		}
		cloudRun.serving["ocel-shop-prod-api"] = "api-3"
		cloudRun.serving["ocel-shop-prod-web"] = "web-3"
		return displaced
	}

	err := pin.MovePointer(context.Background(), cloudRun, move, progress.Discard())

	if !errors.Is(err, displaced) {
		t.Fatalf("MovePointer = %v, want the displacement", err)
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-3" {
		t.Errorf("api serves %s, want the api-3 the displacing promotion pinned: putting a service back must not land over a promotion that moved it since", got)
	}
}

func TestAPinThatLandsButFailsWhileWaitingIsPutBackWithTheRest(t *testing.T) {
	lost := errors.New("waiting on the cloud run operation failed")
	cloudRun := &services{
		serving:   map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		failAfter: map[string]error{"ocel-shop-prod-api": lost},
	}

	err := pin.MovePointer(context.Background(), cloudRun, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, lost) {
		t.Fatalf("MovePointer = %v, want the failure it stopped on", err)
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-1" {
		t.Errorf("api serves %s, want the api-1 it served before: its pin landed before the wait on it failed, so it is put back like any other", got)
	}
}
