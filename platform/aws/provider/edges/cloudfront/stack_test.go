package cloudfront

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPromoteOntoAPointerOtherThanTheDefaultLeavesTheHostnameAlone(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	live := routeOn(t, w, stack, boundHost)
	wrote := w.store.count("kvs.UpdateKeys")

	preview := promotion()
	preview.PromotionID = "p2"
	preview.Builds = map[string]string{"web": "d2.f2"}
	if err := stack.Promote(context.Background(), preview, "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote onto a preview pointer: %v", err)
	}

	if got := w.store.count("kvs.UpdateKeys"); got != wrote {
		t.Errorf("UpdateKeys calls = %d, want %d: only the default pointer owns the hostnames this stack bound", got, wrote)
	}
	if after := routeOn(t, w, stack, boundHost); after.Release != live.Release {
		t.Errorf("the hostname now answers with release %q, want the production release %q: promoting a preview must not repoint production", after.Release, live.Release)
	}
}

func TestRemovePointerLeavesTheHostnameServing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		pointer string
	}{
		{"a preview pointer", "pr-7"},
		{"the default pointer", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			stack := reconciled(t, w)
			bound(t, stack)
			staged(t, stack, fakeEntryURL, fakeAssetPrefix)
			if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardReporter()); err != nil {
				t.Fatalf("Promote: %v", err)
			}

			if _, err := stack.RemovePointer(context.Background(), tc.pointer, edge.DiscardReporter()); err != nil {
				t.Fatalf("RemovePointer: %v", err)
			}

			if published := routeOn(t, w, stack, boundHost); published.Origin != fakeEntryHost {
				t.Errorf("the hostname answers with %q, want the release it was promoted to: only unbinding the domain takes its route away", published.Origin)
			}
		})
	}
}

func TestDomainOwnerAsksTheRouteStoreBeforeListingTheAccount(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	bound(t, stack)
	staged(t, stack, fakeEntryURL, fakeAssetPrefix)
	if err := stack.Promote(context.Background(), promotion(), "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	listed := w.front.count("ListDistributions")

	owner, err := e.DomainOwner(context.Background(), strings.ToUpper(boundHost))
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}

	if owner != productionDistributionName() {
		t.Errorf("DomainOwner(%q) = %q, want the stack the route names (%q)", boundHost, owner, productionDistributionName())
	}
	if got := w.front.count("ListDistributions"); got != listed {
		t.Errorf("ListDistributions calls = %d, want %d: the route store is keyed by hostname, so nothing has to be paged through", got, listed)
	}
}

func TestASecondReconcileRereadsNothingImmutable(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	first, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	before := map[string]int{}
	for _, call := range []string{"ListCachePolicies", "ListResponseHeadersPolicies", "ListOriginAccessControls"} {
		before[call] = w.front.count(call)
	}

	second, err := e.Reconcile(context.Background(), testSpec(), first.State())
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	for _, call := range slices.Sorted(keysOfCount(before)) {
		if got := w.front.count(call); got != before[call] {
			t.Errorf("%s calls = %d, want %d: the stack already names an id CloudFront never changes", call, got, before[call])
		}
	}
	held, want := ownState(t, second), ownState(t, first)
	if held.CachePolicy != want.CachePolicy || held.HeadersPolicy != want.HeadersPolicy || held.OriginAccessControl != want.OriginAccessControl {
		t.Errorf("state = %+v, want the ids the first reconcile recorded (%+v)", held, want)
	}
}

func keysOfCount(held map[string]int) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range held {
			if !yield(key) {
				return
			}
		}
	}
}

func TestDestroyWaitsOutAThrottledRolloutCheck(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	w.front.statusThrottles = 2

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	if len(w.front.distributions) != 0 {
		t.Errorf("%d distributions survived destroy, want none: a throttled status check is not a reason to leave one behind", len(w.front.distributions))
	}
}

func TestDestroyHoldsBeforeItFirstAsksHowTheRolloutIsGoing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := &provider{
		open: func(context.Context) (Clients, error) { return w.clients(), nil },
		settle: Settler{
			Wait: func(context.Context, time.Duration) error {
				w.trail.record("hold")
				return nil
			},
			Attempts: 5,
			Every:    time.Second,
			Jitter:   func() float64 { return 0.5 },
		},
	}
	if _, err := e.Bootstrap(context.Background(), edge.ClassProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	id := ownState(t, stack).Distribution

	if err := stack.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	steps := w.trail.taken()
	disabled := indexOf(t, steps, "UpdateDistribution "+id)
	polled := indexOf(t, steps, "GetDistribution "+id)
	held := slices.Index(steps[disabled:], "hold")
	if held < 0 || disabled+held > polled {
		t.Errorf("the calls were %v, want a hold between disabling the distribution and asking whether it settled", steps)
	}
}

func TestADistributionNamedPastTheCommentCeilingIsStillFoundByName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	name := strings.Repeat("storefront-", 20)
	plan := distributionPlan{
		name:          name,
		assetOrigin:   "assets.s3.eu-west-1.amazonaws.com",
		function:      "arn:aws:cloudfront::111122223333:function/resolver",
		cachePolicy:   "cache-policy",
		headersPolicy: "headers-policy",
		oac:           "origin-access-control",
	}

	raised, err := createDistribution(context.Background(), w.clients(), plan, nil, "")
	if err != nil {
		t.Fatalf("createDistribution: %v", err)
	}
	held := w.front.distributions[raised.id]
	for field, value := range map[string]string{
		"comment":          aws.ToString(held.config.Comment),
		"caller reference": aws.ToString(held.config.CallerReference),
	} {
		if len(value) != maxDistributionNameLen {
			t.Errorf("%s is %d characters, want it clamped to the %d CloudFront accepts", field, len(value), maxDistributionNameLen)
		}
	}

	found, ok, err := findDistribution(context.Background(), w.clients(), name)
	if err != nil {
		t.Fatalf("findDistribution: %v", err)
	}
	if !ok || found.id != raised.id {
		t.Errorf("findDistribution(%q) = %+v, %v, want the distribution just raised (%q)", name, found, ok, raised.id)
	}
}

func TestBindDomainRecordsTheFrontOfADistributionFoundByName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	settled, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	held := w.front.named(productionDistributionName())
	if held == nil {
		t.Fatalf("no distribution for the stack; CloudFront holds %v", w.front.mutations())
	}

	forgotten := settled.State()
	forgotten.Front = ""
	forgotten.Adapter = edge.Own(private{})
	opened, err := e.Open(forgotten)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stack := opened.(*stack)
	if err := stack.BindDomain(context.Background(), edge.DomainBinding{Hostname: boundHost}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	if got := stack.own.Distribution; got != held.id {
		t.Errorf("state records distribution %q, want the one already serving the stack (%q)", got, held.id)
	}
	if got := stack.State().Front; got != held.domain {
		t.Errorf("state records front %q, want %q: a binding onto a distribution found by name still has to say where DNS points", got, held.domain)
	}
}

func TestEveryConfigCarriesLoggingSoAnUpdateIsLegal(t *testing.T) {
	t.Parallel()

	w := newWorld()
	plan := distributionPlan{
		name:          "storefront",
		assetOrigin:   "assets.s3.eu-west-1.amazonaws.com",
		function:      "arn:aws:cloudfront::111122223333:function/resolver",
		cachePolicy:   "cache-policy",
		headersPolicy: "headers-policy",
		oac:           "origin-access-control",
	}

	raised, err := createDistribution(context.Background(), w.clients(), plan, nil, "")
	if err != nil {
		t.Fatalf("createDistribution: %v", err)
	}
	assertLogging(t, "the config sent to CreateDistribution", w.front.distributions[raised.id].config)

	if err := reshapeDistribution(context.Background(), w.clients(), plan, raised.id); err != nil {
		t.Fatalf("reshapeDistribution: %v", err)
	}
	assertLogging(t, "the config sent to UpdateDistribution", w.front.distributions[raised.id].config)
}

func assertLogging(t *testing.T, what string, config *cftypes.DistributionConfig) {
	t.Helper()
	if config.Logging == nil {
		t.Fatalf("%s carries no Logging block, and CloudFront rejects every UpdateDistribution without one", what)
	}
	if aws.ToBool(config.Logging.Enabled) {
		t.Errorf("%s enables access logging, want it off with an empty bucket and prefix", what)
	}
}

func TestEveryConfigSpellsOutTheFieldsAnUpdateInsistsOn(t *testing.T) {
	t.Parallel()

	w := newWorld()
	plan := distributionPlan{
		name:          "storefront",
		assetOrigin:   "assets.s3.eu-west-1.amazonaws.com",
		function:      "arn:aws:cloudfront::111122223333:function/resolver",
		cachePolicy:   "cache-policy",
		headersPolicy: "headers-policy",
		oac:           "origin-access-control",
	}

	raised, err := createDistribution(context.Background(), w.clients(), plan, nil, "")
	if err != nil {
		t.Fatalf("createDistribution: %v", err)
	}
	assertComplete(t, "the config sent to CreateDistribution", w.front.distributions[raised.id].config)

	if err := reshapeDistribution(context.Background(), w.clients(), plan, raised.id); err != nil {
		t.Fatalf("reshapeDistribution: %v", err)
	}
	assertComplete(t, "the config sent to UpdateDistribution", w.front.distributions[raised.id].config)
}

func assertComplete(t *testing.T, what string, config *cftypes.DistributionConfig) {
	t.Helper()
	for field, set := range map[string]bool{
		"DefaultRootObject":    config.DefaultRootObject != nil,
		"Restrictions":         config.Restrictions != nil,
		"WebACLId":             config.WebACLId != nil,
		"CustomErrorResponses": config.CustomErrorResponses != nil,
		"CacheBehaviors":       config.CacheBehaviors != nil,
		"Staging":              config.Staging != nil,
	} {
		if !set {
			t.Errorf("%s leaves %s nil, and UpdateDistribution refuses a config missing it", what, field)
		}
	}
	behavior := config.DefaultCacheBehavior
	if behavior == nil {
		t.Fatalf("%s carries no DefaultCacheBehavior", what)
	}
	if behavior.TrustedSigners == nil || aws.ToBool(behavior.TrustedSigners.Enabled) {
		t.Errorf("%s does not spell out TrustedSigners as disabled", what)
	}
	if behavior.TrustedKeyGroups == nil || aws.ToBool(behavior.TrustedKeyGroups.Enabled) {
		t.Errorf("%s does not spell out TrustedKeyGroups as disabled", what)
	}
}

func TestCompleteFromFillsOnlyWhatThePlanLeftOut(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		want *cftypes.DistributionConfig
		held *cftypes.DistributionConfig
		acl  string
	}{
		{
			name: "the held config supplies a field the plan never mentions",
			want: &cftypes.DistributionConfig{},
			held: &cftypes.DistributionConfig{WebACLId: aws.String("held-acl")},
			acl:  "held-acl",
		},
		{
			name: "the plan wins over a held config that differs",
			want: &cftypes.DistributionConfig{WebACLId: aws.String("")},
			held: &cftypes.DistributionConfig{WebACLId: aws.String("held-acl")},
			acl:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			merged := completeFrom(tc.want, tc.held)

			if merged.WebACLId == nil {
				t.Fatalf("WebACLId is nil, want %q", tc.acl)
			}
			if got := aws.ToString(merged.WebACLId); got != tc.acl {
				t.Errorf("WebACLId = %q, want %q", got, tc.acl)
			}
		})
	}
}

func TestEveryCertificateAConfigCarriesIsOneAnUpdateAccepts(t *testing.T) {
	t.Parallel()

	const (
		alias       = "shop.example.com"
		certificate = "arn:aws:acm:us-east-1:111122223333:certificate/1111-2222"
	)

	w := newWorld()
	plan := distributionPlan{
		name:          "storefront",
		assetOrigin:   "assets.s3.eu-west-1.amazonaws.com",
		function:      "arn:aws:cloudfront::111122223333:function/resolver",
		cachePolicy:   "cache-policy",
		headersPolicy: "headers-policy",
		oac:           "origin-access-control",
	}

	raised, err := createDistribution(context.Background(), w.clients(), plan, nil, "")
	if err != nil {
		t.Fatalf("createDistribution: %v", err)
	}
	assertViewerCertificate(t, "the config sent to CreateDistribution", w.front.distributions[raised.id].config)

	if err := serveAlias(context.Background(), w.clients(), plan, raised.id, alias, certificate); err != nil {
		t.Fatalf("serveAlias: %v", err)
	}
	if got := certificateOf(w.front.distributions[raised.id].config); got != certificate {
		t.Errorf("the distribution serves the alias under certificate %q, want %q", got, certificate)
	}

	if err := dropAlias(context.Background(), w.clients(), plan, raised.id, alias); err != nil {
		t.Fatalf("dropAlias: %v", err)
	}
	dropped := w.front.distributions[raised.id].config
	if len(aliasesOf(dropped)) != 0 {
		t.Errorf("the distribution still answers for %v, want the alias gone", aliasesOf(dropped))
	}
	if !aws.ToBool(dropped.ViewerCertificate.CloudFrontDefaultCertificate) {
		t.Errorf("dropping the last alias left the ACM certificate in place, want the CloudFront default certificate back")
	}

	if err := w.edge().deleteDistribution(context.Background(), w.clients(), kindDistribution, raised.id); err != nil {
		t.Fatalf("deleteDistribution: %v", err)
	}

	if len(w.front.updates) < 3 {
		t.Fatalf("%d configs reached UpdateDistribution, want one each for serving the alias, dropping it and disabling the distribution", len(w.front.updates))
	}
	for i, config := range w.front.updates {
		assertViewerCertificate(t, fmt.Sprintf("the config sent to UpdateDistribution #%d", i+1), config)
	}
}

func assertViewerCertificate(t *testing.T, what string, config *cftypes.DistributionConfig) {
	t.Helper()
	held := config.ViewerCertificate
	if held == nil {
		t.Fatalf("%s carries no ViewerCertificate, and CloudFront rejects an update without one", what)
	}
	if aws.ToBool(held.CloudFrontDefaultCertificate) {
		if held.MinimumProtocolVersion != cftypes.MinimumProtocolVersionTLSv1 {
			t.Errorf("%s takes the default certificate with MinimumProtocolVersion %q, want %q: it is the only one CloudFront accepts there", what, held.MinimumProtocolVersion, cftypes.MinimumProtocolVersionTLSv1)
		}
		if held.SSLSupportMethod != "" {
			t.Errorf("%s takes the default certificate and still names SSLSupportMethod %q, want none", what, held.SSLSupportMethod)
		}
		if aws.ToString(held.ACMCertificateArn) != "" {
			t.Errorf("%s takes the default certificate and still names ACMCertificateArn %q, want none", what, aws.ToString(held.ACMCertificateArn))
		}
		return
	}
	if aws.ToString(held.ACMCertificateArn) == "" {
		t.Errorf("%s takes neither the default certificate nor an ACM one", what)
	}
	if held.SSLSupportMethod != cftypes.SSLSupportMethodSniOnly {
		t.Errorf("%s names SSLSupportMethod %q, want %q", what, held.SSLSupportMethod, cftypes.SSLSupportMethodSniOnly)
	}
	if held.MinimumProtocolVersion == "" {
		t.Errorf("%s names an ACM certificate and no MinimumProtocolVersion, and CloudFront insists on one", what)
	}
}
