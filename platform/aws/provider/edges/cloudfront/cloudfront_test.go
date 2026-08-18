package cloudfront

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"

	"github.com/ocelhq/ocel/platform/aws/provider/edgeledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
)

const (
	conformanceSlug = "conformance"

	entryFunction = "conformance-prod-web-r1234abcd"

	boundHost = "shop.example.com"

	certificateARN = "arn:aws:acm:us-east-1:123456789012:certificate/abcd"
)

func testSpec() edge.StackSpec {
	return edge.StackSpec{Version: "v1", Class: edge.ClassProduction, Slug: conformanceSlug}
}

func productionDistributionName() string {
	return distributionName(conformanceSlug, edge.ClassProduction)
}

func bootstrapped(t *testing.T, w *world) *provider {
	t.Helper()
	e := w.edge()
	if _, err := e.Bootstrap(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return e
}

func TestConformance(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			return bootstrapped(t, newWorld()), testSpec()
		},
		Hostname: boundHost,
		Previews: func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			return previewBootstrapped(t, newWorld()), previewStackSpec(), previewWildcardSpec()
		},
	})
}

func reconciled(t *testing.T, w *world) edge.EdgeStack {
	t.Helper()
	stack, err := bootstrapped(t, w).Reconcile(context.Background(), testSpec(), nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stack
}

func staged(t *testing.T, stack edge.EdgeStack, url, assets string) edge.DeploymentRecord {
	t.Helper()
	record := edge.DeploymentRecord{
		App:           "web",
		Identity:      "d1.f1",
		Entry:         "/",
		EntryFunction: entryFunction,
		FunctionURLs:  map[string]string{"/": url},
		AssetPrefix:   assets,
	}
	if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	return record
}

func bound(t *testing.T, stack edge.EdgeStack) {
	t.Helper()
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{
		Hostname:    boundHost,
		Certificate: certificateARN,
	}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
}

func promotion() edge.Promotion {
	return edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "d1.f1"}}
}

func routeOn(t *testing.T, w *world, stack edge.EdgeStack, hostname string) route {
	t.Helper()
	held := w.store.held(stack.State()[stackKeyKeyValueStore])
	raw, ok := held[hostname]
	if !ok {
		t.Fatalf("the key value store holds %v, want an entry for %q", slices.Sorted(keysOf(held)), hostname)
	}
	var published route
	if err := json.Unmarshal([]byte(raw), &published); err != nil {
		t.Fatalf("decode the route published for %q: %v", hostname, err)
	}
	return published
}

func keysOf(held map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range held {
			if !yield(key) {
				return
			}
		}
	}
}

func indexOf(t *testing.T, steps []string, step string) int {
	t.Helper()
	at := slices.Index(steps, step)
	if at < 0 {
		t.Fatalf("the calls made were %v, want %q among them", steps, step)
	}
	return at
}

func assertSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotSorted, wantSorted := slices.Clone(got), slices.Clone(want)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	if !slices.Equal(gotSorted, wantSorted) {
		t.Errorf("%s = %v, want %v", what, gotSorted, wantSorted)
	}
}

func TestNativeIsNotProgrammable(t *testing.T) {
	t.Parallel()

	var e edge.Edge = newWorld().edge()
	if _, programmable := e.(edge.Programmable); programmable {
		t.Error("the native edge is Programmable, but it declares only edge caching and streaming; nothing of the app's code runs at this edge")
	}
}

func TestBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("it creates the set the edge routes with, and creates it once", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := bootstrapped(t, w)
		if _, err := e.Bootstrap(context.Background(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap again: %v", err)
		}

		assertSet(t, "the mutations bootstrap made", w.front.mutations(), []string{
			"CreateKeyValueStore " + keyValueStoreName(edge.ClassProduction),
			"CreateFunction " + functionName(edge.ClassProduction),
			"PublishFunction " + functionName(edge.ClassProduction),
			"CreateCachePolicy " + cachePolicyName(edge.ClassProduction),
			"CreateResponseHeadersPolicy " + headersPolicyName(edge.ClassProduction),
			"CreateOriginAccessControl " + originAccessControlName(edge.ClassProduction),
		})
	})

	t.Run("the resolver function reads the key value store the same bootstrap made", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		bootstrapped(t, w)

		held := w.front.functions[functionName(edge.ClassProduction)]
		if held == nil {
			t.Fatal("bootstrap published no resolver function")
		}
		if held.config.Runtime != cftypes.FunctionRuntimeCloudfrontJs20 {
			t.Errorf("runtime = %q, want %q, the only one that reads a key value store", held.config.Runtime, cftypes.FunctionRuntimeCloudfrontJs20)
		}
		store := w.front.stores[keyValueStoreName(edge.ClassProduction)]
		if store == nil {
			t.Fatal("bootstrap made no key value store")
		}
		associations := held.config.KeyValueStoreAssociations
		if associations == nil || len(associations.Items) != 1 || aws.ToString(associations.Items[0].KeyValueStoreARN) != store.arn {
			t.Errorf("key value store associations = %+v, want only %q", associations, store.arn)
		}
	})

	t.Run("every response the edge serves names the edge that served it", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		bootstrapped(t, w)

		var marked bool
		for _, config := range w.front.headerPolicy {
			for _, header := range config.CustomHeadersConfig.Items {
				if aws.ToString(header.Header) == edge.HeaderEdge && aws.ToString(header.Value) == string(edge.KindNative) {
					marked = true
				}
			}
		}
		if !marked {
			t.Errorf("no response headers policy sets %s: %s, so a liveness probe cannot tell which front answered", edge.HeaderEdge, edge.KindNative)
		}
	})

	t.Run("an unknown substrate class is refused", func(t *testing.T) {
		t.Parallel()

		if _, err := newWorld().edge().Bootstrap(context.Background(), "staging"); err == nil {
			t.Error("Bootstrap(staging) error = nil, want a refusal naming the class")
		}
	})
}

func TestTeardownRemovesExactlyTheBootstrapSet(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	before := len(w.front.mutations())
	cachePolicy := slices.Sorted(maps.Keys(w.front.cachePolicies))[0]
	headersPolicy := slices.Sorted(maps.Keys(w.front.headerPolicy))[0]
	accessControl := slices.Sorted(maps.Keys(w.front.accessControl))[0]

	if err := e.Teardown(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	assertSet(t, "the mutations teardown made", w.front.mutations()[before:], []string{
		"DeleteFunction " + functionName(edge.ClassProduction),
		"DeleteKeyValueStore " + keyValueStoreName(edge.ClassProduction),
		"DeleteCachePolicy " + cachePolicy,
		"DeleteResponseHeadersPolicy " + headersPolicy,
		"DeleteOriginAccessControl " + accessControl,
	})
	for what, left := range map[string]int{
		"key value stores":          len(w.front.stores),
		"functions":                 len(w.front.functions),
		"cache policies":            len(w.front.cachePolicies),
		"response headers policies": len(w.front.headerPolicy),
		"origin access controls":    len(w.front.accessControl),
	} {
		if left != 0 {
			t.Errorf("%d %s survived teardown, want none", left, what)
		}
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	t.Run("an account that was never bootstrapped is refused by name", func(t *testing.T) {
		t.Parallel()

		_, err := newWorld().edge().Reconcile(context.Background(), testSpec(), nil)
		if err == nil {
			t.Fatal("Reconcile error = nil, want a refusal: nothing fronts this account yet")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("err = %q, want it to name the command that fixes it", err)
		}
	})

	t.Run("it shapes a distribution and publishes the hostname to point at", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)

		held := w.front.named(productionDistributionName())
		if held == nil {
			t.Fatalf("no distribution for the stack; CloudFront holds %v", w.front.mutations())
		}
		if got := stack.State()[stackKeyDistribution]; got != held.id {
			t.Errorf("state records distribution %q, want the one reconcile created (%q)", got, held.id)
		}
		if got := stack.State()[edge.StackKeyFront]; got != held.domain {
			t.Errorf("state records front %q, want the distribution's domain name (%q), which is what a CNAME points at", got, held.domain)
		}

		behavior := held.config.DefaultCacheBehavior
		if aws.ToString(behavior.OriginRequestPolicyId) != allViewerExceptHostPolicyID {
			t.Errorf("origin request policy = %q, want the managed AllViewerExceptHostHeader policy", aws.ToString(behavior.OriginRequestPolicyId))
		}
		if len(behavior.FunctionAssociations.Items) != 1 || behavior.FunctionAssociations.Items[0].EventType != cftypes.EventTypeViewerRequest {
			t.Errorf("function associations = %+v, want the resolver on viewer-request", behavior.FunctionAssociations.Items)
		}
		if aws.ToString(behavior.CachePolicyId) != stack.State()[stackKeyCachePolicy] {
			t.Errorf("cache policy = %q, want the one bootstrap made (%q)", aws.ToString(behavior.CachePolicyId), stack.State()[stackKeyCachePolicy])
		}
		if got := aws.ToString(held.config.CacheTagConfig.HeaderName); got != cacheTagHeader {
			t.Errorf("cache tag header = %q, want %q", got, cacheTagHeader)
		}
		origin := held.config.Origins.Items[0]
		if aws.ToString(origin.DomainName) != assetOriginDomain(fakeAssetBucket, fakeRegion) {
			t.Errorf("origin = %q, want the account's asset bucket", aws.ToString(origin.DomainName))
		}
		if aws.ToString(origin.OriginAccessControlId) != stack.State()[stackKeyOAC] {
			t.Errorf("origin access control = %q, want the one bootstrap made", aws.ToString(origin.OriginAccessControlId))
		}
	})

	t.Run("a second reconcile reaches the distribution it recorded instead of listing them", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := bootstrapped(t, w)
		first, err := e.Reconcile(context.Background(), testSpec(), nil)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		listed := w.front.count("ListDistributions")

		if _, err := e.Reconcile(context.Background(), testSpec(), first.State()); err != nil {
			t.Fatalf("Reconcile again: %v", err)
		}
		if got := w.front.count("ListDistributions"); got != listed {
			t.Errorf("ListDistributions calls = %d, want %d: the distribution id is on the stack, and CloudFront cannot filter that listing", got, listed)
		}
		if got := w.front.count("CreateDistribution"); got != 1 {
			t.Errorf("CreateDistribution calls = %d, want 1", got)
		}
	})

	t.Run("a prune-only stack leaves the front alone", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		spec := testSpec()
		spec.PruneOnly = true
		if _, err := bootstrapped(t, w).Reconcile(context.Background(), spec, nil); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got := w.front.count("CreateDistribution"); got != 0 {
			t.Errorf("CreateDistribution calls = %d, want none: a prune-only stack serves nothing", got)
		}
	})
}

func TestPromote(t *testing.T) {
	t.Parallel()

	t.Run("the store learns the release before the ledger records it", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)

		if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}

		steps := w.trail.taken()
		wrote := indexOf(t, steps, "kvs.UpdateKeys")
		recorded := indexOf(t, steps, "PutItem EDGELEDGER#production/conformance\x00POINTER#@production")
		if wrote > recorded {
			t.Errorf("the ledger pointer moved before the store did (%v); a hostname must never point at a release the edge cannot serve", steps)
		}
	})

	t.Run("the published route names the release, its entry function and its assets", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix+"/")

		if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}

		published := routeOn(t, w, stack, boundHost)
		if published.Origin != fakeEntryHost {
			t.Errorf("origin = %q, want the entry function's URL host %q", published.Origin, fakeEntryHost)
		}
		if published.Release != "d1.f1" {
			t.Errorf("release = %q, want the build the promotion names", published.Release)
		}
		if published.Assets != assetOriginDomain(fakeAssetBucket, fakeRegion) {
			t.Errorf("assets = %q, want the account's asset bucket", published.Assets)
		}
		if published.AssetPrefix != "/"+fakeAssetPrefix {
			t.Errorf("assetPrefix = %q, want %q: CloudFront refuses an origin path that does not start with a slash or that ends with one", published.AssetPrefix, "/"+fakeAssetPrefix)
		}
		if published.Stack != productionDistributionName() {
			t.Errorf("stack = %q, want the stack that owns the hostname (%q)", published.Stack, productionDistributionName())
		}
		if published.Secret != fakeSecret {
			t.Errorf("the route carries a secret the entry function will not accept")
		}
	})

	t.Run("a store that refuses the write records nothing", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		w.store.updateErr = &kvstypes.AccessDeniedException{Message: aws.String("no")}

		if err := stack.Promote(context.Background(), promotion(), ""); err == nil {
			t.Fatal("Promote error = nil, want the refusal the store gave")
		}

		history, err := stack.Ledger().History(context.Background(), "")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 0 {
			t.Errorf("history = %v, want nothing: the store never learned this release", history)
		}
	})

	t.Run("a store another deploy moved is re-read and written again", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		w.store.conflicts = 2

		if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}

		if got := w.store.count("kvs.UpdateKeys"); got != 3 {
			t.Errorf("UpdateKeys calls = %d, want 3: two conflicts and the write that landed", got)
		}
		if got, want := w.store.count("kvs.DescribeKeyValueStore"), 3; got != want {
			t.Errorf("DescribeKeyValueStore calls = %d, want %d: every attempt takes a fresh version", got, want)
		}
		if published := routeOn(t, w, stack, boundHost); published.Origin != fakeEntryHost {
			t.Errorf("origin = %q, want the release to have landed after the conflicts", published.Origin)
		}
	})

	t.Run("a release with no reachable entry function is refused", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, "", fakeAssetPrefix)

		err := stack.Promote(context.Background(), promotion(), "")
		if err == nil {
			t.Fatal("Promote error = nil, want a refusal: nothing names a URL the edge can reach")
		}
		if !strings.Contains(err.Error(), entryFunction) {
			t.Errorf("err = %q, want it to name the entry function it found no URL for", err)
		}
	})

	t.Run("a promotion with no hostname bound writes no route", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)

		if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if got := w.store.count("kvs.UpdateKeys"); got != 0 {
			t.Errorf("UpdateKeys calls = %d, want none: no hostname reaches this project yet", got)
		}
	})
}

func TestUnbindDomainTakesTheRouteAndTheAlias(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(context.Background(), promotion(), ""); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if err := stack.UnbindDomain(context.Background(), boundHost); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}

	if held := w.store.held(stack.State()[stackKeyKeyValueStore]); len(held) != 0 {
		t.Errorf("the key value store holds %v, want nothing once the host is unbound", held)
	}
	distribution := w.front.named(productionDistributionName())
	if len(distribution.config.Aliases.Items) != 0 {
		t.Errorf("aliases = %v, want none once the host is unbound", distribution.config.Aliases.Items)
	}
}

func TestDestroyDisablesTheDistributionBeforeDeletingIt(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	id := stack.State()[stackKeyDistribution]

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	steps := w.front.calls
	disabled := indexOf(t, steps, "UpdateDistribution "+id)
	waited := indexOf(t, steps, "GetDistribution "+id)
	deleted := indexOf(t, steps, "DeleteDistribution "+id)
	if !(disabled < waited && waited < deleted) {
		t.Errorf("the calls were %v, want the distribution disabled, waited out, then deleted", steps)
	}
	if len(w.front.distributions) != 0 {
		t.Errorf("%d distributions survived destroy, want none", len(w.front.distributions))
	}
	if len(w.dynamo.items) != 0 {
		t.Errorf("the ledger left %d items behind, want none", len(w.dynamo.items))
	}
	if got := stack.State()[stackKeyDistribution]; got != "" {
		t.Errorf("state still records distribution %q after destroy", got)
	}
}

func TestDestroyGivesUpOnADistributionStillRollingOut(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := w.edge()
	stack, err := func() (edge.EdgeStack, error) {
		if _, err := e.Bootstrap(context.Background(), edge.ClassProduction); err != nil {
			return nil, err
		}
		return e.Reconcile(context.Background(), testSpec(), nil)
	}()
	if err != nil {
		t.Fatalf("prepare the stack: %v", err)
	}
	w.front.aliasErr = &cftypes.PreconditionFailed{Message: aws.String("busy")}

	destroyErr := stack.Destroy(context.Background())
	if destroyErr == nil {
		t.Fatal("Destroy error = nil, want the refusal the disable attempt gave")
	}
	var stale *cftypes.PreconditionFailed
	if !errors.As(destroyErr, &stale) {
		t.Errorf("err = %v, want it to carry what CloudFront said", destroyErr)
	}
	if !strings.Contains(destroyErr.Error(), "Re-run") {
		t.Errorf("err = %q, want it to say that re-running picks up where this stopped", destroyErr)
	}
}

func TestDestroyKeepsTheLedgerUntilTheDistributionIsActuallyGone(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	id := stack.State()[stackKeyDistribution]
	w.front.rollout = 99

	err := stack.Destroy(context.Background())
	if err == nil {
		t.Fatal("Destroy error = nil, want the run to stop while the distribution is still rolling out")
	}
	var outstanding *edge.OutstandingError
	if !errors.As(err, &outstanding) {
		t.Fatalf("Destroy error = %v, want the distribution named as still standing", err)
	}
	if len(w.front.distributions) != 1 {
		t.Fatalf("%d distributions left, want the one the rollout would not release", len(w.front.distributions))
	}
	if len(w.dynamo.items) == 0 {
		t.Error("the deployments ledger was erased while the distribution it names is still standing; a re-run would never find it")
	}

	w.front.distributions[id].rollout = 0
	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(w.front.distributions) != 0 {
		t.Errorf("%d distributions survived the re-run, want none", len(w.front.distributions))
	}
	if len(w.dynamo.items) != 0 {
		t.Errorf("the ledger left %d items behind after the re-run, want none", len(w.dynamo.items))
	}
}

func TestReconcileLeavesTheTagInvalidatorAFrontToReach(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)

	held := w.invalidationTargets(edgeledger.Scope(edge.ClassProduction, conformanceSlug))
	if held == nil {
		t.Fatalf("the ledger names no front for the tag invalidator to reach; it holds %v", slices.Sorted(maps.Keys(w.dynamo.items)))
	}
	if want := stack.State()[stackKeyDistribution]; !slices.Equal(held, []string{want}) {
		t.Errorf("invalidation targets = %v, want the distribution this reconcile fronts the project with (%q)", held, want)
	}
}

func TestTheFrontKeepsTheOriginsCacheTagsOffTheWire(t *testing.T) {
	t.Parallel()

	w := newWorld()
	reconciled(t, w)

	for id, config := range w.front.headerPolicy {
		removed := config.RemoveHeadersConfig
		if removed == nil {
			t.Fatalf("response headers policy %s removes nothing, so every viewer is told the release and tags of the page it was served", id)
		}
		var names []string
		for _, item := range removed.Items {
			names = append(names, aws.ToString(item.Header))
		}
		if !slices.Contains(names, cacheTagHeader) {
			t.Errorf("response headers policy %s removes %v, want %q among them", id, names, cacheTagHeader)
		}
	}
}

func TestCacheTagHeaderIsTheOneTheOriginWrites(t *testing.T) {
	t.Parallel()

	const shaping = "../../../functions/entrypoints/src/next/cache-shaping.mts"
	source, err := os.ReadFile(shaping)
	if err != nil {
		t.Fatalf("read the origin's cache shaping: %v", err)
	}
	named := regexp.MustCompile(`cacheTagHeader = "([^"]+)"`).FindSubmatch(source)
	if named == nil {
		t.Fatalf("%s no longer names the header the origin puts its cache tags in", shaping)
	}
	if got := string(named[1]); got != cacheTagHeader {
		t.Errorf("the origin writes its tags in %q and the cache policy reads %q, so CloudFront stores no tag at all and every invalidation misses", got, cacheTagHeader)
	}
}

func TestResolverCodeShipsInline(t *testing.T) {
	t.Parallel()

	code := string(ResolverCode())
	if len(code) == 0 {
		t.Fatal("no resolver code is embedded")
	}
	if len(code) > 10*1024 {
		t.Errorf("the resolver is %d bytes, more than the 10 KB CloudFront takes inline", len(code))
	}
	for _, want := range []string{"import cf from 'cloudfront'", "cf.kvs()", "cf.updateRequestOrigin", "async function handler(event)"} {
		if !strings.Contains(code, want) {
			t.Errorf("the resolver does not contain %q", want)
		}
	}
}
