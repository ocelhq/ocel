package alb

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func (w *world) InvalidateTags(_ context.Context, urlMap string, tags []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.invalidating != nil {
		return w.invalidating
	}
	w.invalidatedTags = append(w.invalidatedTags, append([]string{urlMap}, tags...))
	return nil
}

func (w *world) invalidations() [][]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.invalidatedTags)
}

func releasePrefix(release int) string {
	return fmt.Sprintf("production/shop/web/r%08d/isr", release)
}

func stagedRelease(t *testing.T, stack edge.EdgeStack, app, build string, release int, framework string) {
	t.Helper()
	physical := "ocel-shop-prod-" + app
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), router.DeploymentRecord{
		App: app, Build: build, Physical: physical, Framework: framework,
		IsrPrefix: strings.Replace(releasePrefix(release), "/web/", "/"+app+"/", 1),
		Revisions: map[string]string{physical: physical + "-" + build},
	}); err != nil {
		t.Fatalf("PutStaged(%s/%s) = %v", app, build, err)
	}
}

func promoted(t *testing.T, stack edge.EdgeStack, log progress.Log, id, pointer string, builds map[string]string) {
	t.Helper()
	err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{
		Pointer: pointer, Promotion: router.Promotion{PromotionID: id, Builds: builds},
	}, log)
	if err != nil {
		t.Fatalf("MovePointer(%s) = %v", id, err)
	}
}

func served(shared edge.EdgeStack) map[string]servedRelease { return shared.(*stack).recorded.Served }

func TestAPromotionClearsTheReleaseItReplacedFromCloudCDN(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	stagedRelease(t, stack, "web", "b2", 2, buildoutput.FrameworkNext)
	log := &fake.Log{}
	promoted(t, stack, log, "p1", "", map[string]string{"web": "b1"})
	promoted(t, stack, log, "p2", "", map[string]string{"web": "b2"})

	want := [][]string{{"ocel-alb-production-routes", "r00000001"}}
	if got := w.invalidations(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("the promotion invalidated %v, want %v", got, want)
	}
	if want := (servedRelease{Release: "r00000002", Tagged: true}); served(stack)["@production/web"] != want {
		t.Errorf("the edge remembers %v, want web serving %v", served(stack), want)
	}
	if !slices.Contains(log.Lines(), "INFO Cleared release r00000001 from Cloud CDN") {
		t.Errorf("the promotion said %v, want it to say which release it cleared", log.Lines())
	}
}

func TestARollbackClearsTheReleaseItRolledBackFrom(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	stagedRelease(t, stack, "web", "b2", 2, buildoutput.FrameworkNext)
	for _, step := range []struct{ id, build string }{{"p1", "b1"}, {"p2", "b2"}, {"p3", "b1"}} {
		promoted(t, stack, progress.Discard(), step.id, "", map[string]string{"web": step.build})
	}

	want := [][]string{{"ocel-alb-production-routes", "r00000001"}, {"ocel-alb-production-routes", "r00000002"}}
	if got := w.invalidations(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("the promotions invalidated %v, want %v", got, want)
	}
}

func TestTheFirstPromotionOfAnAppClearsNoReleaseTag(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1"})

	if got := w.invalidations(); len(got) != 0 {
		t.Errorf("the first promotion invalidated %v, want nothing: no release was replaced", got)
	}
	if want := (servedRelease{Release: "r00000001", Tagged: true}); served(stack)["@production/web"] != want {
		t.Errorf("the edge remembers %v, want web serving %v", served(stack), want)
	}
}

func TestPromotingTheReleaseAlreadyServedClearsNothing(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1"})
	promoted(t, stack, progress.Discard(), "p2", "", map[string]string{"web": "b1"})

	if got := w.invalidations(); len(got) != 0 {
		t.Errorf("moving a pointer onto the release it already served invalidated %v, want nothing", got)
	}
}

func TestOnePromotionOfTwoNextAppsClearsBothReplacedReleasesInOneRequest(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	for _, app := range []string{"web", "admin"} {
		stagedRelease(t, stack, app, "b1", 1, buildoutput.FrameworkNext)
		stagedRelease(t, stack, app, "b2", 2, buildoutput.FrameworkNext)
	}
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1", "admin": "b1"})
	promoted(t, stack, progress.Discard(), "p2", "", map[string]string{"web": "b2", "admin": "b2"})

	want := [][]string{{"ocel-alb-production-routes", "r00000001", "r00000001"}}
	if got := w.invalidations(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("the promotion invalidated %v, want one request carrying both replaced releases %v", got, want)
	}
}

func TestTwoPreviewPointersOfOneAppNeverClearEachOthersRelease(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	stagedRelease(t, stack, "web", "b2", 2, buildoutput.FrameworkNext)
	promoted(t, stack, progress.Discard(), "p1", "pr-7", map[string]string{"web": "b1"})
	promoted(t, stack, progress.Discard(), "p2", "pr-8", map[string]string{"web": "b2"})

	if got := w.invalidations(); len(got) != 0 {
		t.Errorf("promoting pr-8 invalidated %v, want nothing: pr-7 still serves its release", got)
	}
}

func TestAnAppThatIsNotNextIsNeverClearedByTag(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, "")
	stagedRelease(t, stack, "web", "b2", 2, "")
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1"})
	promoted(t, stack, progress.Discard(), "p2", "", map[string]string{"web": "b2"})

	if got := w.invalidations(); len(got) != 0 {
		t.Errorf("a container app's replacement invalidated %v, want nothing: its responses carry no release tag", got)
	}
	if want := (servedRelease{Release: "r00000002"}); served(stack)["@production/web"] != want {
		t.Errorf("the edge remembers %v, want web serving %v untagged", served(stack), want)
	}
}

func TestARemovedPointerForgetsTheReleaseItServed(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	stagedRelease(t, stack, "web", "b2", 2, buildoutput.FrameworkNext)
	promoted(t, stack, progress.Discard(), "p1", "pr-7", map[string]string{"web": "b1"})
	if err := openRouter(stack).RemovePointer(context.Background(), router.PointerRemoval{Pointer: "pr-7"}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer = %v", err)
	}
	if got := served(stack); len(got) != 0 {
		t.Errorf("the edge remembers %v after pr-7 was removed, want nothing: a record that grows by a key per preview ever promoted never shrinks", got)
	}
	promoted(t, stack, progress.Discard(), "p2", "pr-7", map[string]string{"web": "b2"})

	if got := w.invalidations(); len(got) != 0 {
		t.Errorf("re-promoting pr-7 invalidated %v, want nothing: the removed pointer's release was forgotten", got)
	}
}

func TestADeploymentPointerRecordsNoRelease(t *testing.T) {
	t.Parallel()

	_, w, shared := reconciled(t)
	records := map[string]router.DeploymentRecord{"web": {App: "web", Framework: buildoutput.FrameworkNext, IsrPrefix: releasePrefix(1), Physical: "ocel-shop-prod-web", Revisions: map[string]string{"ocel-shop-prod-web": "rev-1"}}}
	move := router.PointerMove{Pointer: router.FormatDeploymentPointer("pr-7", "p1"), Records: records}
	if err := (routerStack{s: shared.(*stack)}).MovePointer(context.Background(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	if got := served(shared); len(got) != 0 {
		t.Errorf("the edge remembers %v after a deployment pointer moved, want nothing: a deployment hostname serves one immutable release", got)
	}
	if got := w.invalidations(); len(got) != 0 {
		t.Errorf("a deployment pointer invalidated %v, want nothing", got)
	}
}

func TestAPurgeThatFailsWarnsWithTheCommandToClearItAndLeavesThePromotionServed(t *testing.T) {
	t.Parallel()

	_, w, stack := reconciled(t)
	stagedRelease(t, stack, "web", "b1", 1, buildoutput.FrameworkNext)
	stagedRelease(t, stack, "web", "b2", 2, buildoutput.FrameworkNext)
	promoted(t, stack, progress.Discard(), "p1", "", map[string]string{"web": "b1"})
	w.invalidating = fmt.Errorf("permission denied")
	log := &fake.Log{}
	promoted(t, stack, log, "p2", "", map[string]string{"web": "b2"})

	var warned string
	for _, line := range log.Lines() {
		if strings.HasPrefix(line, "WARN ") {
			warned = line
		}
	}
	for _, want := range []string{"invalidate-cdn-cache", "r00000001", "ocel-alb-production-routes", "permission denied"} {
		if !strings.Contains(warned, want) {
			t.Errorf("the warning reads %q, want it to contain %q", warned, want)
		}
	}
	if want := (servedRelease{Release: "r00000002", Tagged: true}); served(stack)["@production/web"] != want {
		t.Errorf("the edge remembers %v, want the promotion recorded as serving %v", served(stack), want)
	}
	if got := w.pins(); !slices.Contains(got, "ocel-shop-prod-web@ocel-shop-prod-web-b2") {
		t.Errorf("the pins are %v, want the second release pinned despite the failed purge", got)
	}
}

func TestAReleaseTagIsTheReleaseTokenOfTheISRPrefix(t *testing.T) {
	t.Parallel()

	for prefix, want := range map[string]string{
		"production/shop/web/r1a2b3c4d/isr":   "r1a2b3c4d",
		"production/shop/web/r1a2b3c4d":       "",
		"production/shop/web/r1a2b3c4d/other": "",
		"production/shop/web/rXYZ/isr":        "",
		"":                                    "",
	} {
		if got := releaseTag(router.DeploymentRecord{IsrPrefix: prefix}); got != want {
			t.Errorf("releaseTag(%q) = %q, want %q", prefix, got, want)
		}
	}
}
