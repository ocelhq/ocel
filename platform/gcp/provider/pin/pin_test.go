package pin_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type services struct {
	serving       map[string]string
	closed        map[string]bool
	refuse        map[string]error
	failAfter     map[string]error
	checkActive   bool
	mu            sync.Mutex
	warmed        []string
	coldStart     error
	rollbacks     map[string]map[string]pin.Rollback
	beforeClose   func(service string)
	beforeRestore func(service string)

	beforeRecordRollback map[string]func()
}

func (s *services) Pin(ctx context.Context, service, revision string, stillActive router.StillActive) (bool, error) {
	if err := s.refuse[service]; err != nil {
		return false, err
	}
	if s.checkActive && stillActive != nil {
		if err := stillActive(ctx); err != nil {
			return false, err
		}
	}
	s.serving[service] = revision
	delete(s.rollbacks[service], revision)
	opened := s.closed[service]
	delete(s.closed, service)
	if err := s.failAfter[service]; err != nil {
		delete(s.failAfter, service)
		return opened, err
	}
	return opened, nil
}

func (s *services) Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	if s.beforeRestore != nil {
		s.beforeRestore(service)
	}
	if s.checkActive && stillActive != nil {
		if err := stillActive(ctx); err != nil {
			return err
		}
	}
	s.serving[service] = revision
	return nil
}

func (s *services) ReadServing(_ context.Context, service string) (string, error) {
	return s.serving[service], nil
}

func (s *services) ReadRollback(_ context.Context, service, revision string) (pin.Rollback, bool, error) {
	rollback, found := s.rollbacks[service][revision]
	return rollback, found, nil
}

func (s *services) RecordRollback(_ context.Context, service, revision string, rollback pin.Rollback) error {
	if hook := s.beforeRecordRollback[service]; hook != nil {
		hook()
	}
	if s.serving[service] == revision {
		return pin.ErrRevisionServed
	}
	if s.rollbacks == nil {
		s.rollbacks = map[string]map[string]pin.Rollback{}
	}
	if s.rollbacks[service] == nil {
		s.rollbacks[service] = map[string]pin.Rollback{}
	}
	s.rollbacks[service][revision] = rollback
	return nil
}

func (s *services) ReadTag(context.Context, string, string) (string, error) { return "", nil }

func (s *services) Untag(context.Context, string, string) error { return nil }

func promotion(builds map[string]string) router.PointerMove {
	move := router.PointerMove{
		Promotion: router.Promotion{PromotionID: "p2", Releases: map[string]string{}},
		Records:   map[string]router.ReleaseRecord{},
	}
	for app, revision := range builds {
		service := "ocel-shop-prod-" + app
		move.Promotion.Releases[app] = "b2"
		move.Records[app] = router.ReleaseRecord{App: app, Release: "b2", Physical: service, Revisions: map[string]string{service: revision}}
	}
	return move
}

func TestAPromotionThatFailsPartWayPutsEveryServiceItPinnedBackOnWhatItServed(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

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
	flaky := restoreRefusing{services: cloudRun}

	_, err := pin.MovePointer(context.Background(), flaky, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	var unserved router.Unserved
	if errors.As(err, &unserved) {
		t.Fatalf("MovePointer = %v, want it served: api still serves api-2, so the ledger must keep naming the promotion that moved it", err)
	}
	if err == nil || !strings.Contains(err.Error(), "ocel-shop-prod-api") || !strings.Contains(err.Error(), "api-2") {
		t.Errorf("MovePointer = %v, want it to say ocel-shop-prod-api is left serving api-2", err)
	}
}

type restoreRefusing struct{ *services }

func (r restoreRefusing) Restore(context.Context, string, string, router.StillActive) error {
	return errors.New("cloud run refused the restore")
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

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, move, progress.Discard())

	if !errors.Is(err, displaced) {
		t.Fatalf("MovePointer = %v, want the displacement", err)
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-3" {
		t.Errorf("api serves %s, want the api-3 the displacing promotion pinned: putting a service back must not land over a promotion that moved it since", got)
	}
}

func TestAPromotionThatFailsAfterAnotherHandedItsServiceBackPutsTheServiceBackOnWhatItServedBefore(t *testing.T) {
	const api = "ocel-shop-prod-api"
	cloudRun := &services{serving: map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"}, checkActive: true}
	displaced := errors.New("another promotion displaced this one")
	move := promotion(map[string]string{"api": "api-2", "web": "web-2"})
	checks := 0
	move.StillActive = func(context.Context) error {
		checks++
		if checks < 3 {
			return nil
		}
		cloudRun.serving[api] = "api-3"
		return displaced
	}
	cloudRun.beforeRecordRollback = map[string]func(){api: func() { cloudRun.serving[api] = "api-2" }}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, move, progress.Discard())

	if !errors.Is(err, displaced) {
		t.Fatalf("MovePointer = %v, want the displacement", err)
	}
	if got := cloudRun.serving[api]; got != "api-1" {
		t.Errorf("api serves %s, want the api-1 it served before: another promotion handed it back to api-2, which this promotion's failure must undo", got)
	}
	if _, recorded := cloudRun.rollbacks[api]["api-2"]; recorded {
		t.Error("a rollback is recorded on api-2, a revision the service serves again")
	}
}

func (s *services) Close(ctx context.Context, service string, stillActive router.StillActive) error {
	if s.beforeClose != nil {
		s.beforeClose(service)
	}
	if stillActive != nil {
		if err := stillActive(ctx); err != nil {
			return err
		}
	}
	if s.closed == nil {
		s.closed = map[string]bool{}
	}
	s.closed[service] = true
	return nil
}

func TestAPromotionThatFailsPartWayClosesAgainTheBrandNewServiceItOpened(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-2", "ocel-shop-prod-web": "web-1"},
		closed:  map[string]bool{"ocel-shop-prod-api": true},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, refused) {
		t.Fatalf("MovePointer = %v, want the refusal that stopped it", err)
	}
	if !cloudRun.closed["ocel-shop-prod-api"] {
		t.Error("api answers everyone after the promotion that opened it failed on web, and no pointer the router records names it, so nothing would close it")
	}
}

func TestTwoPromotionsThatBothFailLeaveClosedTheBrandNewServiceTheFirstOpenedAndTheSecondRepinned(t *testing.T) {
	cloudRun := &services{
		serving:     map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		closed:      map[string]bool{"ocel-shop-prod-api": true},
		checkActive: true,
	}
	displaced := errors.New("another promotion displaced this one")
	first := promotion(map[string]string{"api": "api-2", "web": "web-2"})
	second := promotion(map[string]string{"api": "api-3", "web": "web-3"})
	second.Promotion.PromotionID = "p3"

	secondPinnedAPI := make(chan struct{})
	firstRolledBack := make(chan struct{})
	secondDone := make(chan error)
	secondChecks := 0
	second.StillActive = func(context.Context) error {
		secondChecks++
		if secondChecks < 3 {
			return nil
		}
		close(secondPinnedAPI)
		<-firstRolledBack
		return displaced
	}
	firstChecks := 0
	first.StillActive = func(context.Context) error {
		firstChecks++
		if firstChecks < 3 {
			return nil
		}
		go func() {
			_, err := pin.MovePointer(context.Background(), cloudRun, nil, second, progress.Discard())
			secondDone <- err
		}()
		<-secondPinnedAPI
		return displaced
	}

	if _, err := pin.MovePointer(context.Background(), cloudRun, nil, first, progress.Discard()); !errors.Is(err, displaced) {
		t.Fatalf("MovePointer(first) = %v, want the displacement", err)
	}
	if cloudRun.closed["ocel-shop-prod-api"] {
		t.Fatal("the first promotion closed api while the second still had it pinned")
	}
	close(firstRolledBack)
	if err := <-secondDone; !errors.Is(err, displaced) {
		t.Fatalf("MovePointer(second) = %v, want the displacement", err)
	}

	if !cloudRun.closed["ocel-shop-prod-api"] {
		t.Error("api answers everyone after both promotions that pinned it failed, want it closed: " +
			"the first opened it, and the second put back a revision whose promotion had already failed")
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-1" {
		t.Errorf("api serves %s, want the api-1 it served before either promotion", got)
	}
}

func TestMovingAPointerOffAServiceClosesItWhenNoOtherPointerPinsIt(t *testing.T) {
	ctx := context.Background()
	cloudRun := &services{serving: map[string]string{}}
	both := promotion(map[string]string{"api": "api-2", "web": "web-2"})
	pointers, err := pin.ReplacePointer(ctx, cloudRun, nil, both)
	if err != nil {
		t.Fatalf("ReplacePointer = %v", err)
	}
	other := promotion(map[string]string{"web": "web-2"})
	other.Pointer = "pr-7"
	if pointers, err = pin.ReplacePointer(ctx, cloudRun, pointers, other); err != nil {
		t.Fatalf("ReplacePointer(pr-7) = %v", err)
	}

	if _, err := pin.ReplacePointer(ctx, cloudRun, pointers, promotion(map[string]string{"admin": "admin-1"})); err != nil {
		t.Fatalf("ReplacePointer = %v", err)
	}

	if !cloudRun.closed["ocel-shop-prod-api"] {
		t.Error("api answers everyone after the pointer that pinned it moved to releases that leave it out")
	}
	if cloudRun.closed["ocel-shop-prod-web"] || cloudRun.closed["ocel-shop-prod-admin"] {
		t.Errorf("the move closed %v, want only api: web is still pinned by pr-7 and admin by the move itself", cloudRun.closed)
	}
}

func TestRemovingAPointerClosesEveryServiceItPinnedAndNoOtherPointerPins(t *testing.T) {
	cloudRun := &services{serving: map[string]string{}}
	move := promotion(map[string]string{"api": "api-2", "web": "web-2"})
	pointers, err := pin.MovePointer(context.Background(), cloudRun, nil, move, progress.Discard())
	if err != nil {
		t.Fatalf("MovePointer = %v", err)
	}
	other := promotion(map[string]string{"admin": "admin-1"})
	other.Pointer = "pr-7"
	if pointers, err = pin.MovePointer(context.Background(), cloudRun, pointers, other, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(pr-7) = %v", err)
	}

	pointers, err = pin.ClosePointer(context.Background(), cloudRun, pointers, "")
	if err != nil {
		t.Fatalf("ClosePointer = %v", err)
	}

	for _, service := range []string{"ocel-shop-prod-api", "ocel-shop-prod-web"} {
		if !cloudRun.closed[service] {
			t.Errorf("%s answers everyone after its pointer was removed, want it closed", service)
		}
	}
	if cloudRun.closed["ocel-shop-prod-admin"] {
		t.Error("admin was closed when another pointer was removed, and its own pointer still pins it")
	}
	if _, again := pin.ClosePointer(context.Background(), cloudRun, pointers, ""); again != nil {
		t.Errorf("ClosePointer again = %v, want a removal to be re-entrant", again)
	}
}

func TestAPinThatLandsButFailsWhileWaitingIsPutBackWithTheRest(t *testing.T) {
	lost := errors.New("waiting on the cloud run operation failed")
	cloudRun := &services{
		serving:   map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		closed:    map[string]bool{"ocel-shop-prod-web": true},
		failAfter: map[string]error{"ocel-shop-prod-web": lost},
	}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, lost) {
		t.Fatalf("MovePointer = %v, want the failure it stopped on", err)
	}
	if got := cloudRun.serving["ocel-shop-prod-web"]; got != "web-1" {
		t.Errorf("web serves %s, want the web-1 it served before: its pin landed before the wait on it failed, so it is put back like any other", got)
	}
	if !cloudRun.closed["ocel-shop-prod-web"] {
		t.Error("web answers everyone after the promotion whose pin opened it failed while waiting, want it closed again")
	}
}

func TestAPromotionThatCannotPutBackAServiceItOpenedStillClosesItFirst(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		closed:  map[string]bool{"ocel-shop-prod-api": true},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}
	recorder := &closeRecording{restoreRefusing: restoreRefusing{services: cloudRun}}

	_, err := pin.MovePointer(context.Background(), recorder, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !cloudRun.closed["ocel-shop-prod-api"] {
		t.Error("api answers everyone on api-2 after the promotion that opened it failed and api-1 could not be pinned back, want it closed")
	}
	if err == nil || strings.Contains(err.Error(), "answers everyone") {
		t.Errorf("MovePointer = %v, want it to say api is left on api-2, and not that it answers everyone: it was closed", err)
	}
	if want := []string{"close ocel-shop-prod-api", "repin ocel-shop-prod-api"}; strings.Join(recorder.calls, ",") != strings.Join(want, ",") {
		t.Errorf("restoring api ran %v, want %v: a service closed before the promotion is closed before it is pinned back, "+
			"or it answers everyone on its old revision in between", recorder.calls, want)
	}
}

type closeRecording struct {
	restoreRefusing
	calls []string
}

func (c *closeRecording) Close(ctx context.Context, service string, stillActive router.StillActive) error {
	c.calls = append(c.calls, "close "+service)
	return c.services.Close(ctx, service, stillActive)
}

func (c *closeRecording) Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error {
	c.calls = append(c.calls, "repin "+service)
	return c.restoreRefusing.Restore(ctx, service, revision, stillActive)
}

func TestAPromotionThatFailsAfterAnotherRepinnedTheServiceItOpenedLeavesThatServiceOpen(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		closed:  map[string]bool{"ocel-shop-prod-api": true},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}
	cloudRun.beforeClose = func(service string) { cloudRun.serving[service] = "api-3" }

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, refused) {
		t.Fatalf("MovePointer = %v, want the refusal that stopped it", err)
	}
	if cloudRun.closed["ocel-shop-prod-api"] {
		t.Error("api was closed, but another promotion pinned it since the check: closing it refuses every visitor of that promotion")
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-3" {
		t.Errorf("api serves %s, want the api-3 the other promotion pinned", got)
	}
	if got, ok := cloudRun.rollbacks["ocel-shop-prod-api"]["api-2"]; !ok || !got.Opened {
		t.Errorf("api rollbacks = %+v, want api-2 recorded as having opened the service, so the promotion that pinned it since closes it if it fails", cloudRun.rollbacks["ocel-shop-prod-api"])
	}
}

func TestAPromotionThatCannotCloseAServiceItOpenedSaysItAnswersEveryone(t *testing.T) {
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{"ocel-shop-prod-api": "api-1", "ocel-shop-prod-web": "web-1"},
		closed:  map[string]bool{"ocel-shop-prod-api": true},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}

	_, err := pin.MovePointer(context.Background(), closeRefusing{services: cloudRun}, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if err == nil || !strings.Contains(err.Error(), "ocel-shop-prod-api answers everyone on api-2") {
		t.Errorf("MovePointer = %v, want it to say ocel-shop-prod-api answers everyone on api-2", err)
	}
	if got := cloudRun.serving["ocel-shop-prod-api"]; got != "api-2" {
		t.Errorf("api serves %s, want api-2 left as it is: pinning api-1 back on a service that could not be closed would open the old revision to everyone", got)
	}
}

type closeRefusing struct{ *services }

func (closeRefusing) Close(context.Context, string, router.StillActive) error {
	return errors.New("cloud run refused the close")
}

func (s *services) Warm(_ context.Context, service, revision, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warmed = append(s.warmed, service+"@"+revision+path)
	return s.coldStart
}

func TestWarmingAPromotionAsksEveryRevisionItPinnedOnTheAppsHealthPath(t *testing.T) {
	cloudRun := &services{serving: map[string]string{}}
	move := promotion(map[string]string{"web": "web-2"})
	web := move.Records["web"]
	web.HealthPath = "/healthz"
	web.Revisions["ocel-shop-prod-web-checkout"] = "checkout-2"
	move.Records["web"] = web

	pin.WarmRevisions(context.Background(), cloudRun.Warm, move.Records, progress.Discard())

	want := []string{"ocel-shop-prod-web-checkout@checkout-2/", "ocel-shop-prod-web@web-2/healthz"}
	if got := slices.Sorted(slices.Values(cloudRun.warmed)); !slices.Equal(got, want) {
		t.Errorf("the promotion warmed %v, want %v", got, want)
	}
}

type warnings struct {
	progress.Log
	said []string
}

func (w *warnings) Warn(message string) { w.said = append(w.said, message) }

func TestWarmingSaysWhichRevisionItCouldNotWarm(t *testing.T) {
	cloudRun := &services{serving: map[string]string{}, coldStart: errors.New("warm revision web-2 of ocel-shop-prod-web: connection refused")}
	log := &warnings{Log: progress.Discard()}

	pin.WarmRevisions(context.Background(), cloudRun.Warm, promotion(map[string]string{"web": "web-2"}).Records, log)

	if len(log.said) != 1 || !strings.Contains(log.said[0], "web-2 of ocel-shop-prod-web") {
		t.Errorf("the promotion warned %v, want one warning naming the revision it could not warm", log.said)
	}
}

func TestAPromotionThatFailsWhileOthersKeepRepinningItsServiceRecordsTheRollbackItOwes(t *testing.T) {
	const api = "ocel-shop-prod-api"
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"},
		closed:  map[string]bool{api: true},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}
	closes, records := 0, 0
	cloudRun.beforeClose = func(service string) {
		closes++
		cloudRun.serving[service] = "api-" + string(rune('2'+closes))
	}
	cloudRun.beforeRecordRollback = map[string]func(){api: func() {
		records++
		if records == 1 {
			cloudRun.serving[api] = "api-2"
		}
	}}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, refused) {
		t.Fatalf("MovePointer = %v, want the refusal that stopped it", err)
	}
	if got, ok := cloudRun.rollbacks[api]["api-2"]; !ok || !got.Opened {
		t.Errorf("api rollbacks = %+v, want api-2 recorded as having opened the service: the second close was repinned too, and nothing else owes the rollback", cloudRun.rollbacks[api])
	}
}

func TestAPromotionThatIsRepinnedBeforeItPutsAServiceBackRecordsTheRollbackItOwes(t *testing.T) {
	const api = "ocel-shop-prod-api"
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving:     map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"},
		refuse:      map[string]error{"ocel-shop-prod-web": refused},
		checkActive: true,
	}
	cloudRun.beforeRestore = func(service string) { cloudRun.serving[service] = "api-3" }

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, refused) {
		t.Fatalf("MovePointer = %v, want the refusal that stopped it", err)
	}
	if got, ok := cloudRun.rollbacks[api]["api-2"]; !ok || got.Previous != "api-1" {
		t.Errorf("api rollbacks = %+v, want api-2 recorded as put back on api-1 by the promotion that pinned it since", cloudRun.rollbacks[api])
	}
}

func TestAPromotionThatOthersRepinWithoutEndNamesTheServiceItCouldNotPutBack(t *testing.T) {
	const api = "ocel-shop-prod-api"
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving: map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"},
		closed:  map[string]bool{api: true},
		refuse:  map[string]error{"ocel-shop-prod-web": refused},
	}
	cloudRun.beforeClose = func(service string) { cloudRun.serving[service] = "api-3" }
	cloudRun.beforeRecordRollback = map[string]func(){api: func() { cloudRun.serving[api] = "api-2" }}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if err == nil || !strings.Contains(err.Error(), api+" answers everyone on api-2") {
		t.Errorf("MovePointer = %v, want it to say %s answers everyone on api-2: nothing is recorded, so no later promotion closes it", err, api)
	}
	if strings.Contains(err.Error(), "if the promotion that pinned it since fails") {
		t.Errorf("MovePointer = %v, want no rollback promised: none was recorded", err)
	}
}

func TestAPromotionThatOthersRepinWithoutEndNamesTheServiceItCouldNotPutBackOnItsPreviousRevision(t *testing.T) {
	const api = "ocel-shop-prod-api"
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving:     map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"},
		refuse:      map[string]error{"ocel-shop-prod-web": refused},
		checkActive: true,
	}
	cloudRun.beforeRestore = func(service string) { cloudRun.serving[service] = "api-3" }
	cloudRun.beforeRecordRollback = map[string]func(){api: func() { cloudRun.serving[api] = "api-2" }}

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if err == nil || !strings.Contains(err.Error(), api+" serves api-2") {
		t.Errorf("MovePointer = %v, want it to say %s serves api-2: nothing is recorded, so no later promotion puts it back", err, api)
	}
	if err != nil && strings.Contains(err.Error(), "if the promotion that pinned it since fails") {
		t.Errorf("MovePointer = %v, want no rollback promised: none was recorded", err)
	}
}

type recordRefusing struct{ *services }

func (recordRefusing) RecordRollback(context.Context, string, string, pin.Rollback) error {
	return errors.New("the record store is down")
}

func TestAPromotionWhoseRollbackCannotBeRecordedNamesTheRollbackItOwes(t *testing.T) {
	const api = "ocel-shop-prod-api"
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving:     map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"},
		refuse:      map[string]error{"ocel-shop-prod-web": refused},
		checkActive: true,
	}
	cloudRun.beforeRestore = func(service string) { cloudRun.serving[service] = "api-3" }

	_, err := pin.MovePointer(context.Background(), recordRefusing{services: cloudRun}, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if err == nil || !strings.Contains(err.Error(), api+" is put back on api-2 if the promotion that pinned it since fails") {
		t.Errorf("MovePointer = %v, want it to name %s as put back on api-2 only if the promotion that pinned it since fails", err, api)
	}
}

func TestAPromotionThatClosedAServiceAndIsRepinnedWhilePuttingItBackRecordsARollbackThatDoesNotCloseItAgain(t *testing.T) {
	const api = "ocel-shop-prod-api"
	refused := errors.New("cloud run refused the pin")
	cloudRun := &services{
		serving:     map[string]string{api: "api-1", "ocel-shop-prod-web": "web-1"},
		closed:      map[string]bool{api: true},
		refuse:      map[string]error{"ocel-shop-prod-web": refused},
		checkActive: true,
	}
	cloudRun.beforeRestore = func(service string) { cloudRun.serving[service] = "api-3" }

	_, err := pin.MovePointer(context.Background(), cloudRun, nil, promotion(map[string]string{"api": "api-2", "web": "web-2"}), progress.Discard())

	if !errors.Is(err, refused) {
		t.Fatalf("MovePointer = %v, want the refusal that stopped it", err)
	}
	got, ok := cloudRun.rollbacks[api]["api-2"]
	if !ok || got.Previous != "api-1" || got.Opened {
		t.Errorf("api rollbacks = %+v, want api-2 recorded as put back on api-1 without Opened: this promotion already closed the service", cloudRun.rollbacks[api])
	}
}

func TestAFunctionAppAnswersOnItsEntryFunctionsService(t *testing.T) {
	t.Parallel()

	if got := pin.ResolveService(router.ReleaseRecord{EntryFunction: "fn-svc"}); got != "fn-svc" {
		t.Errorf("ResolveService = %q, want fn-svc", got)
	}
}

func TestAContainerAppAnswersOnItsContainersService(t *testing.T) {
	t.Parallel()

	if got := pin.ResolveService(router.ReleaseRecord{Physical: "ctr", EntryFunction: "fn"}); got != "ctr" {
		t.Errorf("ResolveService = %q, want ctr", got)
	}
}

func TestTheTagsOfAFunctionAppsDeploymentAreReadWithoutRefusal(t *testing.T) {
	t.Parallel()

	tags, err := pin.ReadTags(context.Background(), &services{}, router.ReleaseRecord{
		App: "web", Release: "b1", EntryFunction: "fn-svc", Revisions: map[string]string{"fn-svc": "fn-svc-b1"},
	})
	if err != nil {
		t.Fatalf("ReadTags = %v", err)
	}
	if _, found := tags["fn-svc"]; !found {
		t.Errorf("ReadTags = %v, want a key for fn-svc", tags)
	}
}
