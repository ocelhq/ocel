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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	kvstypes "github.com/aws/aws-sdk-go-v2/service/cloudfrontkeyvaluestore/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront/resolver"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
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
	return distributionName(defaultNamespace, conformanceSlug, edge.ClassProduction)
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
	stack, err := bootstrapped(t, w).Reconcile(context.Background(), testSpec(), edge.StackState{})
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

var fakeFront = awsports.ContainerFront{
	VPCOrigin: "vo_2XyZ3abc4DEF5ghi",
	Host:      "internal-ocel-containers-production-123.eu-west-1.elb.amazonaws.com",
}

func recordFront(t *testing.T, w *world, class edge.Class) {
	t.Helper()
	records := awsports.Records{Dynamo: w.dynamo, Tables: awsports.Table(fakeStateTable)}
	if err := awsports.WriteContainerFront(context.Background(), records, class, fakeFront); err != nil {
		t.Fatalf("WriteContainerFront: %v", err)
	}
}

func stagedContainer(t *testing.T, stack edge.EdgeStack) edge.DeploymentRecord {
	t.Helper()
	record := edge.DeploymentRecord{
		App:         "web",
		Identity:    "d1.f1",
		Image:       "123456789012.dkr.ecr.eu-west-1.amazonaws.com/ocel/web:sha256-abc",
		Physical:    "shop-prod-web-container-r3f8a1c90",
		Origin:      "http://" + fakeFront.Host,
		HealthPath:  "/",
		AssetPrefix: fakeAssetPrefix,
	}
	if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	return record
}

func promotion() edge.Promotion {
	return edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "d1.f1"}}
}

func routeOn(t *testing.T, w *world, stack edge.EdgeStack, hostname string) route {
	t.Helper()
	held := w.store.held(ownState(t, stack).KeyValueStore)
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

func TestTheCloudFrontEdgeRunsNoCode(t *testing.T) {
	t.Parallel()

	if newWorld().edge().Hooks().Compatibility != nil {
		t.Error("the cloudfront edge names a compatibility, but it declares only edge caching and streaming; nothing of the app's code runs at this edge")
	}
}

func TestTheEdgeKindReachesItsBootstrapFeature(t *testing.T) {
	t.Parallel()

	if got := providerkit.FeatureNeedingEdge(bootstrap.Catalogue(), Kind); got != bootstrap.FeatureCloudFrontEdge {
		t.Errorf("bootstrapping with the %q edge raises the %q feature, want %q; nothing else stands the resolver, routes store and policies this edge reads", Kind, got, bootstrap.FeatureCloudFrontEdge)
	}
}

func TestBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("it writes nothing: the bootstrap stack owns the set the edge routes with", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := bootstrapped(t, w)
		if _, err := e.Bootstrap(context.Background(), edge.ClassProduction); err != nil {
			t.Fatalf("Bootstrap again: %v", err)
		}

		assertSet(t, "the mutations bootstrap made", w.front.mutations(), nil)
	})

	t.Run("an unknown class is refused", func(t *testing.T) {
		t.Parallel()

		if _, err := newWorld().edge().Bootstrap(context.Background(), "staging"); err == nil {
			t.Error("Bootstrap(staging) error = nil, want a refusal naming the class")
		}
	})
}

func TestTeardownLeavesTheBootstrapSetToCloudFormation(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	before := len(w.front.mutations())

	if err := e.Teardown(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	assertSet(t, "the mutations teardown made", w.front.mutations()[before:], nil)
	if err := e.Teardown(context.Background(), "staging"); err == nil {
		t.Error("Teardown(staging) error = nil, want a refusal naming the class")
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	t.Run("an account that was never bootstrapped is refused by name", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		w.cfn.absent = true
		_, err := w.edge().Reconcile(context.Background(), testSpec(), edge.StackState{})
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
		if got := ownState(t, stack).Distribution; got != held.id {
			t.Errorf("state records distribution %q, want the one reconcile created (%q)", got, held.id)
		}
		if got := stack.State().Front; got != held.domain {
			t.Errorf("state records front %q, want the distribution's domain name (%q), which is what a CNAME points at", got, held.domain)
		}

		behavior := held.config.DefaultCacheBehavior
		if aws.ToString(behavior.OriginRequestPolicyId) != allViewerExceptHostPolicyID {
			t.Errorf("origin request policy = %q, want the managed AllViewerExceptHostHeader policy", aws.ToString(behavior.OriginRequestPolicyId))
		}
		associated := behavior.FunctionAssociations.Items
		if len(associated) != 2 {
			t.Fatalf("function associations = %+v, want the resolver on viewer-request and the empty-body dropper on viewer-response", associated)
		}
		if associated[0].EventType != cftypes.EventTypeViewerRequest || aws.ToString(associated[0].FunctionARN) != fakeResolverARN(edge.ClassProduction) {
			t.Errorf("first association = %+v, want the resolver on viewer-request", associated[0])
		}
		if associated[1].EventType != cftypes.EventTypeViewerResponse || aws.ToString(associated[1].FunctionARN) != fakeEmptyBodyARN(edge.ClassProduction) {
			t.Errorf("second association = %+v, want the empty-body dropper on viewer-response", associated[1])
		}
		if aws.ToString(behavior.CachePolicyId) != ownState(t, stack).CachePolicy {
			t.Errorf("cache policy = %q, want the one bootstrap made (%q)", aws.ToString(behavior.CachePolicyId), ownState(t, stack).CachePolicy)
		}
		if got := aws.ToString(held.config.CacheTagConfig.HeaderName); got != bootstrap.EdgeCacheTagHeader {
			t.Errorf("cache tag header = %q, want %q", got, bootstrap.EdgeCacheTagHeader)
		}
		origin := held.config.Origins.Items[0]
		if aws.ToString(origin.DomainName) != assetOriginDomain(fakeAssetBucket, fakeRegion) {
			t.Errorf("origin = %q, want the account's asset bucket", aws.ToString(origin.DomainName))
		}
		if aws.ToString(origin.OriginAccessControlId) != ownState(t, stack).OriginAccessControl {
			t.Errorf("origin access control = %q, want the one bootstrap made", aws.ToString(origin.OriginAccessControlId))
		}
	})

	t.Run("a second reconcile reaches the distribution it recorded instead of listing them", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := bootstrapped(t, w)
		first, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
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
		if _, err := bootstrapped(t, w).Reconcile(context.Background(), spec, edge.StackState{}); err != nil {
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

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
			t.Fatalf("Promote: %v", err)
		}

		steps := w.trail.taken()
		wrote := indexOf(t, steps, "kvs.UpdateKeys")
		recorded := indexOf(t, steps, "PutItem ledger#production/conformance\x00pointers#@production#")
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

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
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

	t.Run("a container release is routed through the class front, declared on the distribution as a VPC origin, with no asset bucket in front", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		stagedContainer(t, stack)
		recordFront(t, w, edge.ClassProduction)
		w.front.calls = nil

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
			t.Fatalf("Promote: %v", err)
		}

		published := routeOn(t, w, stack, boundHost)
		if published.Origin != fakeFront.Host {
			t.Errorf("origin = %q, want the front the container answers behind", published.Origin)
		}
		if published.Container != "shop-prod-web-container-r3f8a1c90" {
			t.Errorf("container = %q, want the service the front's rule names: every release shares one front", published.Container)
		}
		if published.Assets != "" || published.AssetPrefix != "" {
			t.Errorf("assets = %q under %q, want none: a container serves its own static files, and a bucket in front would answer 403 for them", published.Assets, published.AssetPrefix)
		}
		if published.Secret != fakeSecret {
			t.Errorf("the route carries no secret, so the origin cannot tell the edge from a stranger")
		}
		held := w.front.named(productionDistributionName())
		if got := containerFrontOf(held.config); got != fakeFront {
			t.Errorf("the distribution declares %+v, want the class front as a VPC origin: the resolver can select it but never rewrite a request to it", got)
		}
		if w.front.count("UpdateDistribution") != 1 || w.front.count("GetDistribution") == 0 {
			t.Errorf("CloudFront saw %v, want one update that declares the origin and a wait for it to roll out before the route goes live", w.front.calls)
		}
		steps := w.trail.taken()
		if indexOf(t, steps, "UpdateDistribution "+held.id) > indexOf(t, steps, "kvs.UpdateKeys") {
			t.Errorf("the route was written before the distribution declared the origin it selects (%v)", steps)
		}

		w.front.calls = nil
		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
			t.Fatalf("Promote (again): %v", err)
		}
		if made := w.front.mutations(); len(made) != 0 {
			t.Errorf("a second container promote changed %v, want the declared origin left alone", made)
		}

		again, err := w.edge().Reconcile(context.Background(), testSpec(), stack.State())
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if got := containerFrontOf(w.front.named(productionDistributionName()).config); got != fakeFront {
			t.Errorf("after a reconcile the distribution declares %+v, want the class front kept: dropping it breaks every container route on the hostname", got)
		}
		if err := again.BindDomain(context.Background(), edge.DomainBinding{Hostname: "www." + boundHost, Certificate: certificateARN}); err != nil {
			t.Fatalf("BindDomain: %v", err)
		}
		if got := containerFrontOf(w.front.named(productionDistributionName()).config); got != fakeFront {
			t.Errorf("after binding a domain the distribution declares %+v, want the class front kept", got)
		}
	})

	t.Run("a container release whose class records no front is refused before the route is written", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		stagedContainer(t, stack)

		err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress())
		if err == nil || !strings.Contains(err.Error(), "container front") {
			t.Fatalf("Promote error = %v, want a refusal naming the missing front", err)
		}
		if _, ok := w.store.held(ownState(t, stack).KeyValueStore)[boundHost]; ok {
			t.Error("a route was written to a front the distribution cannot reach")
		}
	})

	t.Run("a release deployed before a rotation is presented the secret it was deployed with, and one deployed after it the current one", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		rotatedAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		w.ssm.secret = bootstrap.OriginSecret{Current: fakeSecret, CreatedAt: rotatedAt, Previous: "3f7a9c1e5b2d84600f1a2b3c4d5e6f70-old", RotatedAt: rotatedAt}
		stack := reconciled(t, w)
		bound(t, stack)
		record := staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		record.CreatedAt = rotatedAt.Unix() - 1
		if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if published := routeOn(t, w, stack, boundHost); published.Secret != w.ssm.secret.Previous {
			t.Errorf("the route presents %q, want the secret the release was deployed with: it accepts nothing minted after it", published.Secret)
		}

		record.CreatedAt = rotatedAt.Unix()
		if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if published := routeOn(t, w, stack, boundHost); published.Secret != fakeSecret {
			t.Errorf("the route presents %q, want the current secret a release deployed after the rotation accepts", published.Secret)
		}
	})

	t.Run("a store that refuses the write records nothing", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		bound(t, stack)
		staged(t, stack, fakeEntryURL, fakeAssetPrefix)
		w.store.updateErr = &kvstypes.AccessDeniedException{Message: aws.String("no")}

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err == nil {
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

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
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

		err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress())
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

		if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if got := w.store.count("kvs.UpdateKeys"); got != 0 {
			t.Errorf("UpdateKeys calls = %d, want none: no hostname reaches this project yet", got)
		}
	})
}

func TestBoundHostsTakeACNAMEAtTheDistributionDomainName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	bound(t, stack)

	hosts := []string{boundHost}
	records, err := edge.RecordsFor(edge.TargetFor(e, stack.State()), hosts)
	if err != nil {
		t.Fatalf("RecordsFor(%v): %v", hosts, err)
	}
	want := []edge.Record{{
		Name:  boundHost,
		Type:  edge.RecordTypeCNAME,
		Value: w.front.named(productionDistributionName()).domain,
	}}
	if !slices.Equal(records, want) {
		t.Errorf("records = %v, want %v: a distribution answers on a name, so a bound host is a CNAME at it", records, want)
	}
}

func TestUnbindDomainTakesTheRouteAndTheAlias(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardProgress()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if err := stack.UnbindDomain(context.Background(), boundHost); err != nil {
		t.Fatalf("UnbindDomain: %v", err)
	}

	if held := w.store.held(ownState(t, stack).KeyValueStore); len(held) != 0 {
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
	id := ownState(t, stack).Distribution

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	steps := w.front.calls
	disabled := indexOf(t, steps, "UpdateDistribution "+id)
	waited := indexOf(t, steps, "GetDistribution "+id)
	deleted := indexOf(t, steps, "DeleteDistribution "+id)
	if disabled >= waited || waited >= deleted {
		t.Errorf("the calls were %v, want the distribution disabled, waited out, then deleted", steps)
	}
	if len(w.front.distributions) != 0 {
		t.Errorf("%d distributions survived destroy, want none", len(w.front.distributions))
	}
	if len(w.dynamo.items) != 0 {
		t.Errorf("the ledger left %d items behind, want none", len(w.dynamo.items))
	}
	if got := ownState(t, stack).Distribution; got != "" {
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
		return e.Reconcile(context.Background(), testSpec(), edge.StackState{})
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
	id := ownState(t, stack).Distribution
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

	held := w.invalidationTargets(ledger.Scope(edge.ClassProduction, conformanceSlug))
	if held == nil {
		t.Fatalf("the ledger names no front for the tag invalidator to reach; it holds %v", slices.Sorted(maps.Keys(w.dynamo.items)))
	}
	if want := ownState(t, stack).Distribution; !slices.Equal(held, []string{want}) {
		t.Errorf("invalidation targets = %v, want the distribution this reconcile fronts the project with (%q)", held, want)
	}
}

func TestCacheTagHeaderIsTheOneTheOriginWrites(t *testing.T) {
	t.Parallel()

	const shaping = "../../../runtime/src/next/cache-shaping.mts"
	source, err := os.ReadFile(shaping)
	if err != nil {
		t.Fatalf("read the origin's cache shaping: %v", err)
	}
	named := regexp.MustCompile(`cacheTagHeader = "([^"]+)"`).FindSubmatch(source)
	if named == nil {
		t.Fatalf("%s no longer names the header the origin puts its cache tags in", shaping)
	}
	if got := string(named[1]); got != bootstrap.EdgeCacheTagHeader {
		t.Errorf("the origin writes its tags in %q and the cache policy reads %q, so CloudFront stores no tag at all and every invalidation misses", got, bootstrap.EdgeCacheTagHeader)
	}
}

func TestResolverCodeShipsInline(t *testing.T) {
	t.Parallel()

	code := string(resolver.Code())
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
