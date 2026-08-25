package apigateway

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	previewBase    = "preview.example.com"
	previewCert    = "arn:aws:acm:eu-west-1:123456789012:certificate/preview"
	previewWild    = "*." + previewBase
	previewPoint   = "pr1"
	previewEntry   = "conformance-preview-web-r9999zzzz"
	previewAPIName = "ocel--conformance--preview--" + previewPoint
)

func previewStackSpec() edge.StackSpec {
	return edge.StackSpec{Version: "v1", Class: edge.ClassPreview, Slug: conformanceSlug, PruneOnly: true}
}

func previewWildcardSpec() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{
		Version:     "v1",
		BaseDomain:  previewBase,
		Certificate: previewCert,
		GrammarMin:  edge.PreviewGrammarMin,
		GrammarMax:  edge.PreviewGrammarMax,
	}
}

func previewHostname() string {
	return edge.PreviewLabel(conformanceSlug, previewPoint, "") + "." + previewBase
}

func previewing(t *testing.T, w *world) (*provider, edge.EdgeStack) {
	t.Helper()
	ctx := context.Background()
	e := w.edge()
	if _, err := e.Bootstrap(ctx, edge.ClassPreview); err != nil {
		t.Fatalf("Bootstrap(preview): %v", err)
	}
	if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	stack, err := e.Reconcile(ctx, previewStackSpec(), edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	return e, stack
}

func rulesOn(t *testing.T, w *world, domain string) map[string]*fakeRule {
	t.Helper()
	held := w.gateway.domains[domain]
	if held == nil {
		t.Fatalf("no domain name %q; the gateway holds %v", domain, slices.Sorted(maps.Keys(w.gateway.domains)))
	}
	byHost := map[string]*fakeRule{}
	for _, rule := range held.rules {
		byHost[rule.host] = rule
	}
	return byHost
}

func promotePreview(t *testing.T, stack edge.EdgeStack, pointer string) {
	t.Helper()
	ctx := context.Background()
	record := edge.DeploymentRecord{App: "web", Identity: "d1.f1", Entry: "/", EntryFunction: previewEntry}
	if err := stack.Ledger().PutStaged(ctx, record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	promotion := edge.Promotion{PromotionID: "p-" + pointer, Ts: 1, Builds: map[string]string{"web": record.Identity}}
	if err := stack.Promote(ctx, promotion, pointer); err != nil {
		t.Fatalf("Promote(%s): %v", pointer, err)
	}
}

func TestReconcilePreviewWildcardRoutesEverythingUnclaimedTo404(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := w.edge()
	if _, err := e.Bootstrap(ctx, edge.ClassPreview); err != nil {
		t.Fatalf("Bootstrap(preview): %v", err)
	}
	w.gateway.calls = nil

	front, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec())
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}

	assertSet(t, "the calls reconciling the preview wildcard makes", w.gateway.mutations(), []string{
		"CreateDomainName " + previewWild,
		"CreateRoutingRule " + previewWild + " " + anyHost,
	})
	if front != regionalFront(previewWild) {
		t.Errorf("front = %q, want the regional domain name DNS points the wildcard at", front)
	}
	domain := w.gateway.domains[previewWild]
	if domain == nil {
		t.Fatal("no wildcard domain name")
	}
	if domain.routing != agtypes.RoutingModeRoutingRuleOnly {
		t.Errorf("routing mode = %q, want %q; the wildcard carries no base path mapping, only per-preview host rules", domain.routing, agtypes.RoutingModeRoutingRuleOnly)
	}
	if domain.certificate != previewCert {
		t.Errorf("certificate = %q, want the wildcard certificate %q", domain.certificate, previewCert)
	}
	catchAll := rulesOn(t, w, previewWild)[anyHost]
	if catchAll == nil {
		t.Fatalf("no catch-all rule; the wildcard holds %v", w.gateway.mutations())
	}
	if catchAll.api != fakeNotFoundAPI || catchAll.stage != stageName {
		t.Errorf("catch-all rule serves %s/%s, want the %s the core stack outputs, on %s", catchAll.api, catchAll.stage, OutputNotFoundAPIID, stageName)
	}
	if catchAll.priority != catchAllPriority {
		t.Errorf("catch-all priority = %d, want %d; rules are evaluated lowest first, so the catch-all has to lose to every preview", catchAll.priority, catchAllPriority)
	}
}

func TestReconcilePreviewWildcardTwiceChangesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := w.edge()
	if _, err := e.Bootstrap(ctx, edge.ClassPreview); err != nil {
		t.Fatalf("Bootstrap(preview): %v", err)
	}
	first, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec())
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	w.gateway.calls = nil

	second, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec())
	if err != nil {
		t.Fatalf("second ReconcilePreviewWildcard: %v", err)
	}

	if second != first {
		t.Errorf("front = %q, want the %q the first reconcile published", second, first)
	}
	if got := w.gateway.mutations(); len(got) != 0 {
		t.Errorf("the second reconcile called %v, want nothing; it must be re-entrant", got)
	}
}

func TestReconcilePreviewWildcardConvergesADomainThatDrifted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("onto the certificate the bootstrap now holds", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := w.edge()
		if _, err := e.Bootstrap(ctx, edge.ClassPreview); err != nil {
			t.Fatalf("Bootstrap(preview): %v", err)
		}
		stale := previewWildcardSpec()
		stale.Certificate = "arn:aws:acm:eu-west-2:123456789012:certificate/discarded"
		if _, err := e.ReconcilePreviewWildcard(ctx, stale); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}
		w.gateway.calls = nil

		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("second ReconcilePreviewWildcard: %v", err)
		}

		if got := w.gateway.domains[previewWild].certificate; got != previewCert {
			t.Errorf("certificate = %q, want %q: a certificate recorded in another region is discarded and re-requested, and a domain left pinned to the discarded one serves TLS from nothing", got, previewCert)
		}
		assertSet(t, "the calls converging a drifted certificate makes", w.gateway.mutations(), []string{
			"UpdateDomainName " + previewWild,
		})
	})

	t.Run("onto the routing mode every preview rule needs", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		e := w.edge()
		if _, err := e.Bootstrap(ctx, edge.ClassPreview); err != nil {
			t.Fatalf("Bootstrap(preview): %v", err)
		}
		if _, err := w.gateway.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
			DomainName:             aws.String(previewWild),
			RegionalCertificateArn: aws.String(previewCert),
		}); err != nil {
			t.Fatalf("CreateDomainName: %v", err)
		}
		w.gateway.calls = nil

		if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
			t.Fatalf("ReconcilePreviewWildcard: %v", err)
		}

		if got := w.gateway.domains[previewWild].routing; got != agtypes.RoutingModeRoutingRuleOnly {
			t.Errorf("routing mode = %q, want %q: a domain left in the mapping mode takes no routing rule, so every preview promoted onto it would fail", got, agtypes.RoutingModeRoutingRuleOnly)
		}
		assertSet(t, "the calls converging a drifted routing mode makes", w.gateway.mutations(), []string{
			"UpdateDomainName " + previewWild,
			"CreateRoutingRule " + previewWild + " " + anyHost,
		})
	})
}

func TestReconcilePreviewWildcardRefusesAnAbsentBootstrap(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.cfn.absent = true
	_, err := w.edge().ReconcilePreviewWildcard(context.Background(), previewWildcardSpec())
	if err == nil {
		t.Fatal("ReconcilePreviewWildcard succeeded without the not-found API every unclaimed host answers from")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap preview") {
		t.Errorf("error = %v, want it to name the command that raises the bootstrap", err)
	}
	if len(w.gateway.mutations()) != 0 {
		t.Errorf("a refused reconcile called %v, want nothing", w.gateway.mutations())
	}
}

func TestReconcilePreviewWildcardRefusesWhatItCannotServe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("without a base domain", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		spec := previewWildcardSpec()
		spec.BaseDomain = ""
		if _, err := w.edge().ReconcilePreviewWildcard(ctx, spec); err == nil {
			t.Fatal("ReconcilePreviewWildcard succeeded with no base domain")
		}
	})

	t.Run("without a certificate", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		spec := previewWildcardSpec()
		spec.Certificate = ""
		_, err := w.edge().ReconcilePreviewWildcard(ctx, spec)
		if err == nil {
			t.Fatal("ReconcilePreviewWildcard succeeded with no certificate, want the domain name refused rather than raised without TLS")
		}
		if !strings.Contains(err.Error(), "certificate") {
			t.Errorf("error = %v, want it to name what is missing", err)
		}
		if len(w.gateway.mutations()) != 0 {
			t.Errorf("a refused reconcile called %v, want nothing", w.gateway.mutations())
		}
	})
}

func TestPromoteRoutesThePreviewHostAtItsOwnAPI(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, stack := previewing(t, w)
	w.gateway.calls = nil

	promotePreview(t, stack, previewPoint)

	api := w.gateway.named(previewAPIName)
	if api == nil {
		t.Fatalf("no REST API for the preview pointer; the gateway saw %v", w.gateway.mutations())
	}
	rule := rulesOn(t, w, previewWild)[previewHostname()]
	if rule == nil {
		t.Fatalf("no rule for %s; the wildcard holds %v", previewHostname(), slices.Sorted(maps.Keys(rulesOn(t, w, previewWild))))
	}
	if rule.api != api.id || rule.stage != stageName {
		t.Errorf("rule for %s serves %s/%s, want the pointer's own API %s/%s", previewHostname(), rule.api, rule.stage, api.id, stageName)
	}
	if rule.priority >= catchAllPriority {
		t.Errorf("rule priority = %d, want it below the catch-all's %d so the preview beats the 404", rule.priority, catchAllPriority)
	}
	if api.variables[entryVariable] != previewEntry {
		t.Errorf("stage variable %s = %q, want the preview's entry function", entryVariable, api.variables[entryVariable])
	}

	entry := methodOn(api, "/{proxy+}", anyMethod)
	if entry == nil {
		t.Fatalf("the preview API has no catch-all method; methods are %v", slices.Sorted(maps.Keys(api.methods)))
	}
	if entry.integration != agtypes.IntegrationTypeAwsProxy {
		t.Errorf("preview catch-all integration = %q, want AWS_PROXY", entry.integration)
	}
	if entry.transfer != agtypes.ResponseTransferModeStream {
		t.Errorf("preview catch-all response transfer mode = %q, want STREAM; a preview streams its RSC response the same as production", entry.transfer)
	}
	if !strings.HasSuffix(entry.uri, "/response-streaming-invocations") || !strings.Contains(entry.uri, "path/2021-11-15/") {
		t.Errorf("preview catch-all URI = %q, want the streaming invoke action API Gateway requires in STREAM mode", entry.uri)
	}
}

func TestPromoteOffTheGlobalPreviewDomainRoutesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := w.edge()
	if _, err := e.Bootstrap(ctx, edge.ClassPreview); err != nil {
		t.Fatalf("Bootstrap(preview): %v", err)
	}
	if _, err := e.ReconcilePreviewWildcard(ctx, previewWildcardSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	stack, err := e.Reconcile(ctx, previewStackSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}

	promotePreview(t, stack, previewPoint)

	if rule := rulesOn(t, w, previewWild)[previewHostname()]; rule != nil {
		t.Errorf("a project that is not served on the global preview domain got the rule %+v; %s belongs to whoever is", rule, previewHostname())
	}
}

func TestRemovePointerTakesTheRuleAndTheAPI(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	promotePreview(t, stack, previewPoint)
	api := w.gateway.named(previewAPIName)
	w.gateway.calls = nil

	if _, err := stack.RemovePointer(ctx, previewPoint); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}

	assertSet(t, "the calls removing a preview pointer makes", w.gateway.mutations(), []string{
		"DeleteRoutingRule " + previewWild + " " + previewHostname(),
		"DeleteRestApi " + api.id,
	})
	if w.gateway.named(previewAPIName) != nil {
		t.Error("the preview's REST API survived the pointer it served")
	}
	if rule := rulesOn(t, w, previewWild)[previewHostname()]; rule != nil {
		t.Errorf("%s is still routed to %s, so the wildcard answers a preview that is gone", previewHostname(), rule.api)
	}
}

func TestDestroyTakesEveryPreviewItRouted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	_, stack := previewing(t, w)
	for _, pointer := range []string{"pr1", "pr2"} {
		promotePreview(t, stack, pointer)
	}
	w.gateway.calls = nil

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	left := rulesOn(t, w, previewWild)
	for _, pointer := range []string{"pr1", "pr2"} {
		host := edge.PreviewLabel(conformanceSlug, pointer, "") + "." + previewBase
		if left[host] != nil {
			t.Errorf("%s is still routed after the stack that served it was destroyed", host)
		}
		if w.gateway.named(apiName(conformanceSlug, edge.ClassPreview, pointer)) != nil {
			t.Errorf("the REST API for %s survived the destroy", pointer)
		}
	}
	if left[anyHost] == nil {
		t.Error("destroying one project's stack took the catch-all rule with it; the wildcard belongs to the bootstrap, not to a project")
	}
}

func TestDestroyPreviewWildcardTakesTheDomainAndItsRules(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e, stack := previewing(t, w)
	promotePreview(t, stack, previewPoint)
	w.gateway.calls = nil

	if err := e.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}

	assertSet(t, "the calls destroying the preview wildcard makes", w.gateway.mutations(), []string{
		"DeleteRoutingRule " + previewWild + " " + anyHost,
		"DeleteRoutingRule " + previewWild + " " + previewHostname(),
		"DeleteDomainName " + previewWild,
	})
	if w.gateway.domains[previewWild] != nil {
		t.Error("the wildcard domain name survived its destroy")
	}
	w.gateway.calls = nil
	if err := e.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Fatalf("second DestroyPreviewWildcard: %v", err)
	}
	assertSet(t, "the calls a second destroy makes", w.gateway.mutations(), []string{
		"DeleteDomainName " + previewWild,
	})
}

func TestPreviewHostRulesDoNotCollideOnPriority(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, stack := previewing(t, w)
	pointers := []string{"pr1", "pr2", "pr3", "review-a", "review-b"}
	for _, pointer := range pointers {
		promotePreview(t, stack, pointer)
	}

	seen := map[int32]string{}
	for host, rule := range rulesOn(t, w, previewWild) {
		if held, taken := seen[rule.priority]; taken {
			t.Errorf("%s and %s both sit at priority %d; API Gateway refuses two rules at one priority", host, held, rule.priority)
		}
		seen[rule.priority] = host
	}
	if len(seen) != len(pointers)+1 {
		t.Errorf("rules = %d, want one per preview plus the catch-all", len(seen))
	}
}
