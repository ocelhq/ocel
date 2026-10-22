package alb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var shopGate = gate.Gate{Key: "shop-key", PasswordHash: gate.SignPassword("shop-key", "shop password"), BypassSecrets: []string{"shop-bypass"}}

var blogGate = gate.Gate{Key: "blog-key", PasswordHash: gate.SignPassword("blog-key", "blog password")}

var tiers = []environment.Tier{environment.TierPreview, environment.TierProduction}

func gatedEdge(t *testing.T) (*Edge, *world) {
	t.Helper()
	return gatedEdgeOn(t, environment.TierPreview)
}

func gatedEdgeOn(t *testing.T, tiers ...environment.Tier) (*Edge, *world) {
	t.Helper()
	balancer, w := balancing(t)
	for _, tier := range tiers {
		if _, err := balancer.Bootstrap(context.Background(), tier); err != nil {
			t.Fatalf("Bootstrap(%s) = %v", tier, err)
		}
	}
	return balancer, w
}

func reconcileGated(t *testing.T, balancer *Edge, slug string, held *gate.Gate) *stack {
	t.Helper()
	return reconcileGatedOn(t, balancer, environment.TierPreview, slug, held)
}

func reconcileGatedOn(t *testing.T, balancer *Edge, tier environment.Tier, slug string, held *gate.Gate) *stack {
	t.Helper()
	reconciled, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: slug, Tier: tier, Gate: held}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(%s, %s) = %v", tier, slug, err)
	}
	return reconciled.(*stack)
}

func TestBootstrappingEachTierAttachesAFailClosedGateExtensionToItsForwardingRule(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()

			_, w := gatedEdgeOn(t, tier)

			declared := w.declarations(GateStack(tier))
			extension, found := declared["ocel-"+naming.TierWord(tier)+"-alb-gate-edge"]
			if !found || extension.Token != lbEdgeExtensionToken {
				t.Fatalf("the gate stack declares %v, want a load balancer edge extension: it runs before the url map and before Cloud CDN, on every request", keys(declared))
			}
			rules, _ := extension.Args["forwardingRules"].([]any)
			if len(rules) != 1 || !strings.HasSuffix(rules[0].(string), "/global/forwardingRules/ocel-"+naming.TierWord(tier)+"-alb-forward") {
				t.Errorf("the extension attaches to %v, want the tier's https forwarding rule", rules)
			}
			chains, _ := extension.Args["extensionChains"].([]any)
			chain, _ := chains[0].(map[string]any)
			extensions, _ := chain["extensions"].([]any)
			plugin, _ := extensions[0].(map[string]any)
			if plugin["failOpen"] != false {
				t.Errorf("the extension has failOpen = %v, want false: a plugin that crashes refuses requests rather than passing them ungated", plugin["failOpen"])
			}
			if events, _ := plugin["supportedEvents"].([]any); len(events) != 1 || events[0] != "REQUEST_HEADERS" {
				t.Errorf("the extension runs on %v, want REQUEST_HEADERS alone", events)
			}
			if got := chain["matchCondition"].(map[string]any)["celExpression"]; got != "true" {
				t.Errorf("the extension chain matches %v, want every request", got)
			}
		})
	}
}

func TestEveryForwardingRuleThatServesContentRunsTheGateBeforeCloudCDN(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()

			_, w := gatedEdgeOn(t, tier)

			front := w.declarations(LoadBalancerStack(tier))
			extension := w.declarations(GateStack(tier))["ocel-"+naming.TierWord(tier)+"-alb-gate-edge"]
			attached, _ := extension.Args["forwardingRules"].([]any)
			rules := 0
			for name, declared := range front {
				if declared.Token != "gcp:compute/globalForwardingRule:GlobalForwardingRule" {
					continue
				}
				rules++
				if !slices.ContainsFunc(attached, func(rule any) bool { return strings.HasSuffix(rule.(string), "/forwardingRules/"+name) }) {
					t.Errorf("forwarding rule %s reaches the url map and its Cloud CDN backends without the gate extension, which runs before the cache lookup: a cached response would reach a visitor the gate refuses", name)
				}
			}
			if rules == 0 {
				t.Fatal("the load balancer declares no forwarding rule, and this test proves nothing")
			}
		})
	}
}

func TestThePluginIsUploadedOnceToTheTiersRegistryAndTheTierRunsThatImage(t *testing.T) {
	t.Parallel()
	for _, tier := range tiers {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()

			_, w := gatedEdgeOn(t, tier)

			if w.gate.uploads[tier] != 1 || len(w.gate.uploads) != 1 {
				t.Errorf("the bootstrap ensured the plugin images %v, want once in the %s tier's repository", w.gate.uploads, tier)
			}
			declared := w.declarations(GateStack(tier))
			versions, _ := declared["ocel-"+naming.TierWord(tier)+"-alb-gate"].Args["versions"].([]any)
			version, _ := versions[0].(map[string]any)
			if image, _ := version["imageUri"].(string); !strings.HasPrefix(image, pluginRepository(tier)) {
				t.Errorf("the plugin runs image %q, want the generic artifact in the tier's own repository, in resource-name form", image)
			}
		})
	}
}

func TestAnEmptyGateSetStillAttachesThePluginSoGatingLaterIsAVersionSwapAlone(t *testing.T) {
	t.Parallel()

	_, w := gatedEdge(t)

	if config := w.gateConfig(GateStack(environment.TierPreview)); config == "" {
		t.Fatal("the plugin holds no config, and a plugin that cannot start refuses every request")
	}
	if hosts := w.gatedHosts(GateStack(environment.TierPreview)); len(hosts) != 0 {
		t.Errorf("the plugin gates %v on a tier no project gates", hosts)
	}
}

func TestTheALBEdgeGates(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	if !balancer.Facts().Gates {
		t.Error("Facts().Gates = false, and a gated preview is refused on an edge that does not run the gate")
	}
}

func TestTheALBEdgeChecksSignedPasswordsSinceItsPluginCannotDeriveScrypt(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)
	if !balancer.Facts().ChecksSignedPasswords {
		t.Error("Facts().ChecksSignedPasswords = false, but a Service Extensions plugin has 1 ms of CPU a request, too little to derive an scrypt hash")
	}
}

func TestAGatedPreviewsKeysAndHostsReachThePluginBeforeAnyRequestIsRoutedToIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	shared := reconcileGated(t, balancer, "shop", &shopGate)
	router := routerStack{s: shared}

	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	if hosts := w.gatedHosts(GateStack(environment.TierPreview)); !slices.Equal(hosts, []string{aliasHost}) {
		t.Errorf("the plugin gates %v, want %s", hosts, aliasHost)
	}
	events := w.happened()
	gatedAt, probedAt, routedAt := lastIndex(events, "up "+GateStack(environment.TierPreview)), indexOf(events, "probe "+aliasHost), indexOf(events, "route "+aliasHost)
	if gatedAt < 0 || gatedAt >= probedAt || probedAt >= routedAt {
		t.Errorf("events %v, want the gate published, then %s probed until it answered 401, then %s routed", events, aliasHost, aliasHost)
	}
	if !strings.Contains(events[probedAt], "at "+loadBalancerAddress) {
		t.Errorf("the probe %q does not go to the load balancer's address %s: it must reach the plugin, not whatever the name resolves to", events[probedAt], loadBalancerAddress)
	}
}

func TestAHostBoundToAGatedPreviewIsInThePluginBeforeItIsRoutedToTheApp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	shared := reconcileGated(t, balancer, "shop", &shopGate)
	const bound = "pr-7-web.preview.example.com"
	if err := shared.BindDomain(ctx, edge.DomainBinding{Hostname: bound, App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	if err := (routerStack{s: shared}).MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	if hosts := w.gatedHosts(GateStack(environment.TierPreview)); !slices.Contains(hosts, bound) {
		t.Errorf("the plugin gates %v, want %s, which now routes to the preview", hosts, bound)
	}
	events := w.happened()
	probedAt, routedAt := indexOf(events, "probe "+bound), indexOf(events, "route "+bound)
	if routedAt < 0 || probedAt < 0 || probedAt > routedAt {
		t.Errorf("events %v, want %s proven gated before it was routed", events, bound)
	}
}

func TestAHostBoundToAGatedPreviewThatIsAlreadyReleasedIsProvenGatedBeforeItIsRouted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	shared := reconcileGated(t, balancer, "shop", &shopGate)
	stage(t, shared, previewRecord("b1"))
	promote(t, shared, "p1", "b1")
	const bound = "pr-7-web.preview.example.com"

	if err := shared.BindDomain(ctx, edge.DomainBinding{Hostname: bound, App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	events := w.happened()
	probedAt, routedAt := indexOf(events, "probe "+bound), indexOf(events, "route "+bound)
	if routedAt < 0 || probedAt < 0 || probedAt > routedAt {
		t.Errorf("events %v, want %s proven gated before it was routed", events, bound)
	}
}

func TestAGatedPreviewIsWarmedWithASessionTheGateAdmits(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	shared := reconcileGated(t, balancer, "shop", &shopGate)
	stage(t, shared, previewRecord("b1"))
	promote(t, shared, "p1", "b1")
	const bound = "pr-7-web.preview.example.com"
	if err := shared.BindDomain(ctx, edge.DomainBinding{Hostname: bound, App: "web"}); err != nil {
		t.Fatalf("BindDomain = %v", err)
	}

	stage(t, shared, previewRecord("b2"))
	promote(t, shared, "p2", "b2")

	w.mu.Lock()
	cookies := slices.Clone(w.warmCookies)
	w.mu.Unlock()
	if len(cookies) == 0 {
		t.Fatal("the promotion warmed nothing")
	}
	verdict := shopGate.Check(gate.Request{Method: "HEAD", Host: bound, Target: "/", Header: gate.Header{"Cookie": {cookies[len(cookies)-1]}}}, time.Now())
	if verdict.Response != nil {
		t.Errorf("the warm carried %q, which the gate refuses with %d, so it warms nothing", cookies[len(cookies)-1], verdict.Response.Status)
	}
}

func TestAPreviewHostIsRoutedOnlyOnceTheGateAnswersItsUncredentialedRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	w.leaveUnenforced(aliasHost, 3)

	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	if got := w.probed(aliasHost); got < 4 {
		t.Errorf("the host was probed %d times, want it polled past the 3 answers that were not the gate's", got)
	}
	if w.waited() == 0 {
		t.Error("the probe never waited between attempts")
	}
}

func TestAGateThatIsNeverEnforcedFailsThePromotionAndRoutesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	w.leaveUnenforced(aliasHost, 1_000_000)

	err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard())

	if err == nil {
		t.Fatal("MovePointer succeeded for a host whose gate no request ever met")
	}
	if !strings.Contains(err.Error(), aliasHost) {
		t.Errorf("the error %q does not name %s", err, aliasHost)
	}
	if routed := eventsMentioning(w.happened(), "route "+aliasHost); len(routed) != 0 {
		t.Errorf("events %v, want the host left unrouted: an ungated preview would be served to anyone", routed)
	}
}

func TestAProbeThatCannotReachTheLoadBalancerFailsTheGatedPromotion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	w.gate.probeErr = errProbeRefused

	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err == nil {
		t.Fatal("MovePointer succeeded without a single probe getting through")
	}
}

func TestAPreviewWithNoGateIsNeitherPublishedNorProbed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	ups := len(w.happened())
	router := routerStack{s: reconcileGated(t, balancer, "shop", nil)}

	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatalf("MovePointer = %v", err)
	}

	if got := w.probed(aliasHost); got != 0 {
		t.Errorf("an ungated host was probed %d times", got)
	}
	if again := eventsMentioning(w.happened()[ups:], "up "+GateStack(environment.TierPreview)); len(again) != 0 {
		t.Errorf("the plugin was republished %v for a project that gates nothing", again)
	}
}

func TestADeployWhoseGateIsUnchangedPublishesNoNewVersion(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdge(t)
	reconcileGated(t, balancer, "shop", &shopGate)
	ups := len(eventsMentioning(w.happened(), "up "+GateStack(environment.TierPreview)))

	reconcileGated(t, balancer, "shop", &shopGate)

	if now := len(eventsMentioning(w.happened(), "up "+GateStack(environment.TierPreview))); now != ups {
		t.Errorf("the gate stack was raised %d times, want %d: the same keys are the same version", now, ups)
	}
}

func TestChangingAGateAddsAVersionAndKeepsTheOneItReplacedForTheSwitch(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdge(t)
	reconcileGated(t, balancer, "shop", &shopGate)
	rotated := shopGate
	rotated.BypassSecrets = []string{"rotated"}

	reconcileGated(t, balancer, "shop", &rotated)

	versions, _ := w.declarations(GateStack(environment.TierPreview))["ocel-preview-alb-gate"].Args["versions"].([]any)
	if len(versions) != 2 {
		t.Errorf("the plugin lists %d versions, want the new one and the one it replaces: Google will not delete the main version, and older ones are pruned", len(versions))
	}
}

func TestARaiseThatFailedKeepsEveryVersionThePluginMayStillRun(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdge(t)
	reconcileGated(t, balancer, "shop", &shopGate)
	running := w.declarations(GateStack(environment.TierPreview))["ocel-preview-alb-gate"].Args["mainVersionId"]
	failed := shopGate
	failed.BypassSecrets = []string{"failed"}
	w.failUps(GateStack(environment.TierPreview), errors.New("the update was interrupted"))
	if _, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Tier: environment.TierPreview, Gate: &failed}, edge.StackState{}); err == nil {
		t.Fatal("Reconcile succeeded through a failed raise")
	}
	next := shopGate
	next.BypassSecrets = []string{"next"}

	reconcileGated(t, balancer, "shop", &next)

	versions, _ := w.declarations(GateStack(environment.TierPreview))["ocel-preview-alb-gate"].Args["versions"].([]any)
	var names []any
	for _, each := range versions {
		names = append(names, each.(map[string]any)["versionName"])
	}
	if len(names) != 3 || !slices.Contains(names, running) {
		t.Errorf("the plugin lists versions %v, want the new one, the one the failed raise declared and %v, which may still be the one running", names, running)
	}
}

func TestPublishingManyTimesNeverKeepsMoreThanTwoVersions(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdge(t)
	for i := range 6 {
		rotated := shopGate
		rotated.BypassSecrets = []string{strings.Repeat("x", i+1)}
		reconcileGated(t, balancer, "shop", &rotated)
	}

	versions, _ := w.declarations(GateStack(environment.TierPreview))["ocel-preview-alb-gate"].Args["versions"].([]any)
	if len(versions) != 2 {
		t.Errorf("the plugin lists %d versions after six publishes, want 2", len(versions))
	}
}

func TestRemovingAGateRemovesItsHostsFromThePluginWithoutProbing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatal(err)
	}
	probes := w.probed(aliasHost)

	if _, err := balancer.Reconcile(ctx, edge.StackSpec{Slug: "shop", Tier: environment.TierPreview}, router.s.State()); err != nil {
		t.Fatalf("Reconcile without a gate = %v", err)
	}

	if hosts := w.gatedHosts(GateStack(environment.TierPreview)); len(hosts) != 0 {
		t.Errorf("the plugin still gates %v after the gate was removed", hosts)
	}
	if got := w.probed(aliasHost); got != probes {
		t.Errorf("removing the gate probed the host %d more times, and there is nothing to prove enforced", got-probes)
	}
}

func TestTwoProjectsGatesDeployedAtOnceAreBothInThePluginAtTheEnd(t *testing.T) {
	t.Parallel()

	balancer, w := gatedEdge(t)
	w.onUp = func() {
		reconcileGated(t, balancer, "blog", &blogGate)
	}

	reconcileGated(t, balancer, "shop", &shopGate)

	config := w.gateConfig(GateStack(environment.TierPreview))
	for _, key := range []string{"shop-key", "blog-key"} {
		if !strings.Contains(config, key) {
			t.Errorf("the final plugin config lacks %s: a deploy that raced another published a snapshot without it\n%s", key, config)
		}
	}
}

type watchedKeyValues struct {
	records.Store
	onWrite func(key records.Key)
}

func (w watchedKeyValues) Write(ctx context.Context, entry records.Entry) (records.Revision, error) {
	revision, err := w.Store.Write(ctx, entry)
	w.onWrite(entry.Key)
	return revision, err
}

func TestAPublicationThatLostARaceRecordsThePluginVersionThatIsRunning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	var shopPublishing atomic.Bool
	shopWrote, blogWrote, blogUp := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var closeShopWrote, closeBlogWrote sync.Once
	balancer.deps.KeyValues = watchedKeyValues{Store: balancer.deps.KeyValues, onWrite: func(key records.Key) {
		if key.String() != gatePublicationKey(environment.TierPreview).String() || !shopPublishing.Load() {
			return
		}
		select {
		case <-shopWrote:
			closeBlogWrote.Do(func() { close(blogWrote) })
		default:
			closeShopWrote.Do(func() { close(shopWrote) })
			<-blogWrote
		}
	}}
	blogDone := make(chan error, 1)
	w.beforeNextUp(func() {
		w.afterNextUp(func() {
			close(blogUp)
			<-shopWrote
		})
		go func() {
			_, err := balancer.Reconcile(ctx, edge.StackSpec{Slug: "blog", Tier: environment.TierPreview, Gate: &blogGate}, edge.StackState{})
			blogDone <- err
		}()
		<-blogUp
		shopPublishing.Store(true)
	})

	reconcileGated(t, balancer, "shop", &shopGate)
	if err := <-blogDone; err != nil {
		t.Fatalf("Reconcile(blog) = %v", err)
	}
	reconcileGated(t, balancer, "shop", &shopGate)

	config := w.gateConfig(GateStack(environment.TierPreview))
	for _, key := range []string{"shop-key", "blog-key"} {
		if !strings.Contains(config, key) {
			t.Errorf("the plugin runs a config without %s after blog's raise finished before shop's and recorded after it:\n%s", key, config)
		}
	}
}

func TestAGateThatOutgrowsThePluginPackageIsRefusedWithWhatToTrim(t *testing.T) {
	t.Parallel()

	balancer, _ := gatedEdge(t)
	huge := shopGate
	huge.BypassSecrets = []string{strings.Repeat("s", 6*1024*1024)}

	_, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Tier: environment.TierPreview, Gate: &huge}, edge.StackState{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("Reconcile = %v, want a refusal", err)
	}
	if !strings.Contains(refused.Message, "bypass") {
		t.Errorf("the refusal reads %q, want what to trim", refused.Message)
	}
}

func TestAGatedDeployOnATierWhoseLoadBalancerRunsNoPluginIsRefusedUntilItIsBootstrapped(t *testing.T) {
	t.Parallel()

	balancer, _ := balancing(t)

	_, err := balancer.Reconcile(context.Background(),
		edge.StackSpec{Slug: "shop", Tier: environment.TierPreview, Gate: &shopGate}, edge.StackState{})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Reconcile = %v, want a not-ready refusal", err)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("the refusal reads %q, want it to say to run `ocel bootstrap`", refused.Message)
	}
}

func TestTearingDownATierDeletesOnlyItsOwnPluginRepositoryAndGates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdgeOn(t, tiers...)
	reconcileGatedOn(t, balancer, environment.TierPreview, "shop", &shopGate)

	if err := balancer.Teardown(ctx, environment.TierProduction); err != nil {
		t.Fatalf("Teardown(production) = %v", err)
	}

	if w.gate.deletes[environment.TierProduction] != 1 || w.gate.deletes[environment.TierPreview] != 0 {
		t.Errorf("the teardown deleted the plugin repositories %v, want the production tier's alone: what a bootstrap uploads its teardown removes, and the preview tier still runs its plugin", w.gate.deletes)
	}
	if _, found, err := balancer.readGateRecord(ctx, environment.TierPreview, "shop"); err != nil || !found {
		t.Errorf("the preview gate of shop = %t, %v after production was torn down, want it kept", found, err)
	}
	if len(w.declarations(GateStack(environment.TierPreview))) == 0 {
		t.Error("tearing production down took the preview tier's gate stack with it")
	}
}

func TestDestroyingAGatedProjectRemovesItsGateFromThePlugin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatal(err)
	}

	if err := router.s.Destroy(ctx); err != nil {
		t.Fatalf("Destroy = %v", err)
	}

	if config := w.gateConfig(GateStack(environment.TierPreview)); strings.Contains(config, "shop-key") {
		t.Errorf("the plugin still holds the destroyed project's keys:\n%s", config)
	}
}

func TestDestroyingAGatedProjectUnroutesItsHostsBeforeTheGateLetsThemThrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatal(err)
	}
	before := len(w.happened())

	if err := router.s.Destroy(ctx); err != nil {
		t.Fatalf("Destroy = %v", err)
	}

	events := w.happened()[before:]
	unroutedAt, ungatedAt := indexOf(events, "unroute "+aliasHost), indexOf(events, "up "+GateStack(environment.TierPreview))
	if unroutedAt < 0 || ungatedAt < 0 || ungatedAt < unroutedAt {
		t.Errorf("events %v, want %s unrouted before the gate is published without it: in between it serves the preview to anyone", events, aliasHost)
	}
}

func TestADestroyThatCannotUnrouteAGatedHostKeepsItsGate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	router := routerStack{s: reconcileGated(t, balancer, "shop", &shopGate)}
	if err := router.MovePointer(ctx, aliasMove("p1", aliasHost, previewRecord("b1")), progress.Discard()); err != nil {
		t.Fatal(err)
	}
	w.unrouteFails[aliasHost] = errors.New("the url map is being updated")

	if err := router.s.Destroy(ctx); err == nil {
		t.Fatal("Destroy succeeded with a host still routed")
	}

	if hosts := w.gatedHosts(GateStack(environment.TierPreview)); !slices.Equal(hosts, []string{aliasHost}) {
		t.Errorf("the plugin gates %v, want %s still gated: it is still routed", hosts, aliasHost)
	}
}

func TestThePluginConfigIsNeverHeldInThePlainByTheKeyValueStore(t *testing.T) {
	t.Parallel()

	balancer, _ := gatedEdge(t)
	reconcileGated(t, balancer, "shop", &shopGate)

	entries, err := balancer.deps.KeyValues.List(context.Background(), gatePartition(environment.TierPreview))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no gate record was kept")
	}
	for _, entry := range entries {
		for _, secret := range []string{"shop-key", "shop-mac", "shop-bypass"} {
			if strings.Contains(string(entry.Value), secret) {
				t.Errorf("%s holds %q in the plain", entry.Key, secret)
			}
		}
	}
}

func TestEveryPreviewBackendAndTheAssetsBucketMarkTheirResponsesNoindex(t *testing.T) {
	t.Parallel()

	preview := bindingProgram(bindingSpec{
		Namespace: "ocel",
		Region:    "europe-west1", Slug: "shop", Tier: environment.TierPreview, CertificateMap: "certs",
		Hosts: map[string]Host{aliasHost: {Service: previewService, Backend: must(backendName("ocel", "shop", environment.TierPreview, aliasHost))}},
	})
	seen, err := declared(preview)
	if err != nil {
		t.Fatal(err)
	}
	backend := seen[must(backendName("ocel", "shop", environment.TierPreview, aliasHost))]
	if headers, _ := backend.Args["customResponseHeaders"].([]any); len(headers) != 1 || headers[0] != "X-Robots-Tag: noindex" {
		t.Errorf("a preview backend adds %v to its responses, want X-Robots-Tag: noindex: the plugin cannot add a header to a response", headers)
	}

	front, err := declared(loadBalancerProgram(loadBalancerSpec{
		Namespace: "ocel",
		Names:     loadBalancerNames("ocel", environment.TierPreview), Tier: environment.TierPreview, Assets: &tierAssets,
	}))
	if err != nil {
		t.Fatal(err)
	}
	bucket := front["ocel-preview-alb-assets"]
	if headers, _ := bucket.Args["customResponseHeaders"].([]any); len(headers) != 1 || headers[0] != "X-Robots-Tag: noindex" {
		t.Errorf("the preview assets bucket adds %v, want X-Robots-Tag: noindex", headers)
	}
}

func TestProductionBackendsAndAssetsAddNoHeaderToTheirResponses(t *testing.T) {
	t.Parallel()

	seen, err := declared(binding(map[string]Host{"shop.example.com": {Service: "svc", Backend: "shop-backend"}}))
	if err != nil {
		t.Fatal(err)
	}
	if headers := seen["shop-backend"].Args["customResponseHeaders"]; headers != nil {
		t.Errorf("a production backend adds %v, want none", headers)
	}
	front, err := declared(loadBalancerProgram(frontSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if headers := front[assetsBackend].Args["customResponseHeaders"]; headers != nil {
		t.Errorf("the production assets bucket adds %v, want none", headers)
	}
}

func TestThePluginConfigIsMarkedSecretInThePulumiState(t *testing.T) {
	t.Parallel()

	secret, err := pluginConfigIsSecret(gateProgram(gateSpec{
		Names: loadBalancerNames("ocel", environment.TierPreview),
		Main:  gateVersion{ID: "v1", Image: "projects/p/locations/r/repositories/x/genericArtifacts/y:z", Config: "ocel-gate 1\n"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !secret {
		t.Error("the plugin config is declared in the plain, and Pulumi state would hold the gates' keys readable")
	}
}

func indexOf(events []string, prefix string) int {
	return slices.IndexFunc(events, func(event string) bool { return strings.HasPrefix(event, prefix) })
}

func lastIndex(events []string, prefix string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if strings.HasPrefix(events[i], prefix) {
			return i
		}
	}
	return -1
}

func TestPublishingAGateSendsItToThePluginWithoutAnotherDeploy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	state := reconcileGated(t, balancer, "shop", nil).State()
	var said []string

	err := balancer.Hooks().PublishGate(ctx, edge.GatePublication{State: state, Gate: &shopGate, Say: func(line string) { said = append(said, line) }})

	if err != nil {
		t.Fatalf("PublishGate = %v", err)
	}
	if config := w.gateConfig(GateStack(environment.TierPreview)); !strings.Contains(config, shopGate.Key) {
		t.Errorf("the plugin runs %q, want a config holding the published gate's key", config)
	}
	if recorded, found, err := balancer.readGateRecord(ctx, environment.TierPreview, "shop"); err != nil || !found || recorded.Gate.Key != shopGate.Key {
		t.Errorf("recorded gate = %+v, %t, %v, want the published one", recorded.Gate, found, err)
	}
	if len(said) == 0 {
		t.Error("publishing said nothing, and a plugin rollout takes minutes")
	}
}

func TestThePublishedGateAnswersTheConformanceSuite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, _ := gatedEdge(t)
	state := reconcileGated(t, balancer, "shop", nil).State()

	edgeconformance.RunPublishedGate(t, edgeconformance.PublishedGate{
		Publish: func(ctx context.Context, held *gate.Gate) error {
			return balancer.Hooks().PublishGate(ctx, edge.GatePublication{State: state, Gate: held})
		},
		Read: func(context.Context) (*gate.Gate, error) {
			recorded, found, err := balancer.readGateRecord(ctx, environment.TierPreview, "shop")
			if err != nil || !found {
				return nil, err
			}
			return &recorded.Gate, nil
		},
		Signed: true,
	})
}

func TestDestroyingAGatedProjectAgainAfterAFailedWithdrawalRollsTheGateOutOfThePlugin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	balancer, w := gatedEdge(t)
	stack := reconcileGated(t, balancer, "shop", &shopGate)
	w.failUps(GateStack(environment.TierPreview), errors.New("the rollout failed"))

	if err := stack.Destroy(ctx); err == nil {
		t.Fatal("Destroy = nil, want the failed rollout")
	}
	if config := w.gateConfig(GateStack(environment.TierPreview)); !strings.Contains(config, shopGate.Key) {
		t.Fatalf("the plugin runs %q after the failed rollout, want the gate still in it", config)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy (retry) = %v", err)
	}

	if config := w.gateConfig(GateStack(environment.TierPreview)); strings.Contains(config, shopGate.Key) {
		t.Errorf("the plugin still holds the destroyed project's keys after the retry:\n%s", config)
	}
}

var productionGate = gate.Gate{Key: "shop-production-key", PasswordHash: gate.SignPassword("shop-production-key", "shop production password")}
