package apigateway

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/edgeconformance"
)

const (
	conformanceSlug = "conformance"

	entryFunction = "conformance-prod-web-r1234abcd"
)

func testSpec() edge.StackSpec {
	return edge.StackSpec{Version: "v1", Class: edge.ClassProduction, Slug: conformanceSlug}
}

func productionAPIName() string { return apiName(conformanceSlug, edge.ClassProduction, "") }

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
		Hostname: "shop.example.com",
		Previews: func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			w := newWorld()
			e := w.edge()
			if _, err := e.Bootstrap(context.Background(), edge.ClassPreview); err != nil {
				t.Fatalf("Bootstrap(preview): %v", err)
			}
			return e, previewStackSpec(), previewWildcardSpec()
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

func staged(t *testing.T, stack edge.EdgeStack, function, assets string) edge.DeploymentRecord {
	t.Helper()
	record := edge.DeploymentRecord{
		App:           "web",
		Identity:      "d1.f1",
		Entry:         "/",
		EntryFunction: function,
		AssetPrefix:   assets,
	}
	if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	return record
}

func methodOn(api *fakeAPI, path, httpMethod string) *fakeMethod {
	for id, held := range api.resources {
		if held == path {
			return api.methods[id+" "+httpMethod]
		}
	}
	return nil
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

func TestNoneIsNotProgrammable(t *testing.T) {
	t.Parallel()

	var e edge.Edge = newWorld().edge()
	if _, programmable := e.(edge.Programmable); programmable {
		t.Error("the api-gateway edge is Programmable, but it declares only streaming; nothing of the app's code runs at this edge")
	}
}

func TestTheEdgeKindReachesItsBootstrapFeature(t *testing.T) {
	t.Parallel()

	if got := providerkit.FeatureNeedingEdge(bootstrap.Catalogue(), Kind); got != bootstrap.FeatureAPIGatewayEdge {
		t.Errorf("bootstrapping with the %q edge raises the %q feature, want %q; nothing else stands the invoke role and the not-found API this edge fronts deployments with", Kind, got, bootstrap.FeatureAPIGatewayEdge)
	}
}

func TestReconcileShapesTheProductionAPI(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)

	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatalf("no REST API named for the stack; gateway holds %v", w.gateway.mutations())
	}
	if ownState(t, stack).API != api.id {
		t.Errorf("state records API %q, want the one reconcile created (%q)", ownState(t, stack).API, api.id)
	}
	assertSet(t, "resources", slices.Collect(maps.Values(api.resources)), []string{
		"/", "/{proxy+}", "/_next", "/_next/static", "/_next/static/{proxy+}",
	})
	if !slices.Contains(api.binary, "*/*") {
		t.Errorf("binary media types = %v, want */* so the static assets survive the trip", api.binary)
	}

	entry := methodOn(api, "/{proxy+}", anyMethod)
	if entry == nil {
		t.Fatalf("no catch-all method; methods are %v", slices.Sorted(maps.Keys(api.methods)))
	}
	if entry.integration != agtypes.IntegrationTypeAwsProxy {
		t.Errorf("catch-all integration = %q, want AWS_PROXY", entry.integration)
	}
	if entry.transfer != agtypes.ResponseTransferModeStream {
		t.Errorf("catch-all response transfer mode = %q, want STREAM; the entry function answers as a stream", entry.transfer)
	}
	if !strings.Contains(entry.uri, "function:${stageVariables."+entryVariable+"}") {
		t.Errorf("catch-all URI = %q, want it to name the entry stage variable", entry.uri)
	}
	if !strings.HasSuffix(entry.uri, "/response-streaming-invocations") || !strings.Contains(entry.uri, "path/2021-11-15/") {
		t.Errorf("catch-all URI = %q, want the streaming invoke action API Gateway requires in STREAM mode", entry.uri)
	}
	if entry.credentials == "" || !strings.Contains(entry.credentials, "role/ocel-edge-invoke") {
		t.Errorf("catch-all credentials = %q, want the execution role on the integration", entry.credentials)
	}

	static := methodOn(api, "/_next/static/{proxy+}", getMethod)
	if static == nil {
		t.Fatal("no static-asset method")
	}
	if static.integration != agtypes.IntegrationTypeAws {
		t.Errorf("static integration = %q, want the S3 service integration", static.integration)
	}
	if static.transfer == agtypes.ResponseTransferModeStream {
		t.Error("static integration streams, want it buffered so API Gateway maps its response headers")
	}
	if !strings.Contains(static.uri, "s3:path/"+fakeAssetBucket+"/${stageVariables."+assetsVariable+"}") {
		t.Errorf("static URI = %q, want the asset bucket under the release's prefix", static.uri)
	}
}

func TestOnlyTheRoutesThatCanCarryTheEdgeHeaderDeclareIt(t *testing.T) {
	t.Parallel()

	w := newWorld()
	reconciled(t, w)

	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatal("no REST API for the stack")
	}
	for _, path := range []string{"/", "/{proxy+}"} {
		entry := methodOn(api, path, anyMethod)
		if len(entry.methodResponses) != 0 {
			t.Errorf("the entry method on %s declares the response %v; API Gateway ignores method responses on a proxy integration, so the entry function is what sets %s", path, slices.Sorted(maps.Keys(entry.methodResponses)), EdgeHeader)
		}
	}
	static := methodOn(api, "/_next/static/{proxy+}", getMethod)
	if got := static.integrationResponse["200"][edgeHeaderParameter]; got != "'"+string(Kind)+"'" {
		t.Errorf("static integration response sets %s = %q, want 'api-gateway'", EdgeHeader, got)
	}
}

func TestPromoteMovesTheStageOnce(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "assets/shop/web/r1234abcd")

	promotion := edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": record.Identity}}
	if err := stack.Promote(context.Background(), promotion, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if got := w.gateway.count("UpdateStage"); got != 1 {
		t.Errorf("UpdateStage calls = %d, want exactly one; promoting is moving one stage", got)
	}
	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != record.EntryFunction {
		t.Errorf("stage variable %s = %q, want the entry function %q", entryVariable, api.variables[entryVariable], record.EntryFunction)
	}
	if api.variables[assetsVariable] != record.AssetPrefix {
		t.Errorf("stage variable %s = %q, want %q", assetsVariable, api.variables[assetsVariable], record.AssetPrefix)
	}
	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 || history[0].PromotionID != "p1" || !history[0].Active {
		t.Errorf("history = %v, want the promotion just made, in effect", history)
	}
}

func TestPromoteServesTheFunctionTheDeployNamed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := edge.DeploymentRecord{
		App:           "web",
		Identity:      "d1.f1",
		Entry:         "/",
		EntryFunction: entryFunction,
	}
	if err := stack.Ledger().PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	promotion := edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": record.Identity}}
	if err := stack.Promote(context.Background(), promotion, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != entryFunction {
		t.Errorf("stage variable %s = %q, want the Lambda the deploy created, not the route it serves", entryVariable, api.variables[entryVariable])
	}
	if api.variables[assetsVariable] != unsetVariable {
		t.Errorf("stage variable %s = %q, want it cleared where the release built no static prefix; the previous release's objects must not survive the promotion", assetsVariable, api.variables[assetsVariable])
	}
}

func TestPromoteRefusesWhatTheStageCannotServe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("a release the ledger never staged", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)

		err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "d1.f1"}}, "", edge.DiscardReporter())
		if err == nil {
			t.Fatal("Promote succeeded against a promotion nothing was staged for")
		}
		assertNothingRecorded(t, w, stack)
	})

	t.Run("a record from before the entry function was recorded", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		record := staged(t, stack, "", "assets/one")

		err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": record.Identity}}, "", edge.DiscardReporter())
		if err == nil {
			t.Fatal("Promote succeeded on a record naming no entry function")
		}
		if !strings.Contains(err.Error(), "entry function") {
			t.Errorf("error = %v, want it to name what the record is missing", err)
		}
		assertNothingRecorded(t, w, stack)
	})

	t.Run("more apps than one stage can serve", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		for _, app := range []string{"api", "web"} {
			record := edge.DeploymentRecord{App: app, Identity: "d1.f1", Entry: "/", EntryFunction: "conformance-prod-" + app + "-r1234abcd"}
			if err := stack.Ledger().PutStaged(ctx, record); err != nil {
				t.Fatalf("PutStaged(%s): %v", app, err)
			}
		}

		promotion := edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"api": "d1.f1", "web": "d1.f1"}}
		err := stack.Promote(ctx, promotion, "", edge.DiscardReporter())
		if err == nil {
			t.Fatal("Promote succeeded on two apps, want the one-app limit surfaced rather than one app silently served")
		}
		for _, want := range []string{"api", "web", "cloudflare"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
		assertNothingRecorded(t, w, stack)
	})
}

func assertNothingRecorded(t *testing.T, w *world, stack edge.EdgeStack) {
	t.Helper()
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none behind a promotion the edge refused", got)
	}
	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != unsetVariable {
		t.Errorf("stage variable %s = %q, want the stage left where it was", entryVariable, api.variables[entryVariable])
	}
	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("history = %v, want nothing recorded behind a stage that never moved", history)
	}
}

func TestPromoteRecordsNothingWhenTheStageRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "assets/shop/web/r1234abcd")
	w.gateway.stageErr = errors.New("stage is being updated by another operation")

	promotion := edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": record.Identity}}
	if err := stack.Promote(context.Background(), promotion, "", edge.DiscardReporter()); err == nil {
		t.Fatal("Promote succeeded, want the stage failure surfaced")
	}

	history, err := stack.Ledger().History(context.Background(), "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("history = %v, want nothing recorded behind a stage that never moved", history)
	}
}

func TestRollbackMovesTheStageOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	first := edge.DeploymentRecord{App: "web", Identity: "d1.f1", Entry: "/", EntryFunction: "conformance-prod-web-r1111aaaa", AssetPrefix: "assets/one"}
	second := edge.DeploymentRecord{App: "web", Identity: "d2.f2", Entry: "/", EntryFunction: "conformance-prod-web-r2222bbbb", AssetPrefix: "assets/two"}
	for _, record := range []edge.DeploymentRecord{first, second} {
		if err := stack.Ledger().PutStaged(ctx, record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": first.Identity}}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": second.Identity}}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}

	before := w.gateway.count("UpdateStage")
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 3, Builds: map[string]string{"web": first.Identity}}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("rollback to p1: %v", err)
	}
	if got := w.gateway.count("UpdateStage") - before; got != 1 {
		t.Errorf("UpdateStage calls for the rollback = %d, want exactly one", got)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != first.EntryFunction {
		t.Errorf("stage variable %s = %q, want the rolled-back release %q", entryVariable, api.variables[entryVariable], first.EntryFunction)
	}
	history, err := stack.Ledger().History(ctx, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 2 || history[0].PromotionID != "p1" || !history[0].Active {
		t.Errorf("history = %v, want p1 back in effect over two promotions", history)
	}
}

func TestRollbackRecordsNothingWhenTheStageRefuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "assets/one")
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": record.Identity}}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	w.gateway.stageErr = errors.New("stage is being updated by another operation")

	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": record.Identity}}, "", edge.DiscardReporter()); err == nil {
		t.Fatal("rollback succeeded, want the stage failure surfaced")
	}
	history, err := stack.Ledger().History(ctx, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 || history[0].PromotionID != "p1" {
		t.Errorf("history = %v, want only the promotion that did move the stage", history)
	}
}

func TestReconcileRepairsAnAPIThatWasNeverFinished(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	w.gateway.resourceErr = errors.New("Too Many Requests")

	if _, err := e.Reconcile(ctx, testSpec(), edge.StackState{}); err == nil {
		t.Fatal("Reconcile succeeded while API Gateway refused to add a resource")
	}
	half := w.gateway.named(productionAPIName())
	if half == nil {
		t.Fatal("the half-created API is gone; the repair this test covers cannot happen")
	}
	if half.stage != "" {
		t.Errorf("stage = %q, want none; a shaping that failed must not leave a stage behind", half.stage)
	}

	w.gateway.resourceErr = nil
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := w.gateway.count("CreateRestApi"); got != 1 {
		t.Errorf("CreateRestApi calls = %d, want one; the second reconcile must re-shape what it found rather than raise another API", got)
	}
	api := w.gateway.named(productionAPIName())
	if ownState(t, stack).API != api.id {
		t.Errorf("state records API %q, want the one left behind (%q)", ownState(t, stack).API, api.id)
	}
	assertSet(t, "resources", slices.Collect(maps.Values(api.resources)), []string{
		"/", "/{proxy+}", "/_next", "/_next/static", "/_next/static/{proxy+}",
	})
	if api.stage != stageName {
		t.Errorf("stage = %q, want %q; the repair has to publish what it shaped", api.stage, stageName)
	}
	if api.variables[entryVariable] != unsetVariable {
		t.Errorf("stage variable %s = %q, want it unset until a promotion names a release", entryVariable, api.variables[entryVariable])
	}
}

func TestReconcileKeepsTheReleaseTheStageIsServing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	record := staged(t, stack, entryFunction, "assets/one")
	if err := stack.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": record.Identity}}, "", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if _, err := e.Reconcile(ctx, testSpec(), stack.State()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != record.EntryFunction {
		t.Errorf("stage variable %s = %q, want the release in effect left alone by a reconcile", entryVariable, api.variables[entryVariable])
	}
}

func TestAPINamesCannotCollideAcrossSlugsAndPointers(t *testing.T) {
	t.Parallel()

	slugged := apiName("shop-pr1", edge.ClassProduction, "")
	pointed := apiName("shop", edge.ClassProduction, "pr1")
	if slugged == pointed {
		t.Errorf("apiName is %q for both a slug and a pointer that end the same; two projects would share one API", slugged)
	}
	if got := apiName("shop", edge.ClassProduction, ""); got != "ocel--shop--production" {
		t.Errorf("apiName = %q, want the project stem the rest of the deploy path matches on", got)
	}
	if name := bootstrap.EdgeNotFoundAPIName(edge.ClassProduction); deploy.ProjectOwnsWorker("not", name) || strings.Contains(name, "--") {
		t.Errorf("the not-found API is named %q, which a project could claim as its own", name)
	}
}

func TestDomainOwnerNamesTheProjectThatHoldsTheHost(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	owner, err := e.DomainOwner(ctx, "shop.example.com")
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if !deploy.ProjectOwnsWorker(conformanceSlug, owner) {
		t.Errorf("DomainOwner = %q, which %q is not recognised as owning; preflight would report the project's own host as claimed by someone else", owner, conformanceSlug)
	}
	if deploy.ProjectOwnsWorker("other", owner) {
		t.Errorf("DomainOwner = %q, which another project is recognised as owning", owner)
	}
}

func TestDestroyTakesTheAPIAndItsDomains(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: "shop.example.com", Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	api := w.gateway.named(productionAPIName())
	w.gateway.calls = nil

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	assertSet(t, "the calls destroy makes", w.gateway.mutations(), []string{
		"DeleteBasePathMapping shop.example.com",
		"DeleteDomainName shop.example.com",
		"DeleteRestApi " + api.id,
	})
	if w.gateway.named(productionAPIName()) != nil {
		t.Error("the project's REST API survived its destroy")
	}
	if bound := stack.State().Bound; len(bound) != 0 {
		t.Errorf("bound domains = %v, want none", bound)
	}
}

func TestDestroyErasesTheDeploymentsLedger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	staged(t, stack, "arn:aws:lambda:eu-west-1:123456789012:function:entry", "assets/")

	if !slices.ContainsFunc(slices.Collect(maps.Keys(w.dynamo.items)), func(key string) bool {
		return strings.HasPrefix(key, "ledger#")
	}) {
		t.Fatal("nothing was staged into the ledger, so its erasure proves nothing")
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	for key := range w.dynamo.items {
		if strings.HasPrefix(key, "ledger#") {
			t.Errorf("ledger row %q survived the destroy", key)
		}
	}
}

func TestTeardownLeavesTheCoreStacksResourcesToCloudFormation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		t.Run(string(class), func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			e := w.edge()
			if _, err := e.Bootstrap(ctx, class); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			w.gateway.calls = nil

			if err := e.Teardown(ctx, class); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			if got := w.gateway.calls; len(got) != 0 {
				t.Errorf("teardown called %v, want nothing; the role and the 404 responder go down with the core stack", got)
			}
		})
	}

	t.Run("an unknown class is refused rather than reported done", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		if err := w.edge().Teardown(ctx, edge.Class("staging")); err == nil {
			t.Fatal("Teardown(staging) reported success having removed nothing")
		}
		if len(w.gateway.calls) != 0 {
			t.Errorf("an unknown class called %v, want nothing", w.gateway.calls)
		}
	})
}

func TestBootstrapWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	out, err := w.edge().Bootstrap(ctx, edge.ClassProduction)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if out.Trust != edge.TrustInternal {
		t.Errorf("Trust = %q, want internal; this edge runs inside the account it serves", out.Trust)
	}
	if len(out.Offers) != 0 {
		t.Errorf("Offers = %v, want none; the api-gateway edge hosts no store the bootstrap adopts", out.Offers)
	}
	if got := w.gateway.mutations(); len(got) != 0 {
		t.Errorf("bootstrap called %v, want nothing; everything it once created is in the core stack, where the change plan can show it", got)
	}
}

func TestReconcileRefusesAnAccountThatWasNeverBootstrapped(t *testing.T) {
	t.Parallel()

	t.Run("without the bootstrap", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		w.cfn.absent = true

		if _, err := w.edge().Reconcile(context.Background(), testSpec(), edge.StackState{}); err == nil {
			t.Fatal("Reconcile succeeded against a bootstrap that was never bootstrapped")
		}
	})

	t.Run("without the invoke role", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		w.cfn.otherEdge = true
		_, err := w.edge().Reconcile(context.Background(), testSpec(), edge.StackState{})
		if err == nil {
			t.Fatal("Reconcile succeeded without the role API Gateway invokes through")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("error = %v, want it to name the command that creates the role", err)
		}
	})
}

func TestReconcileTakesTheInvokeRoleFromTheCoreStack(t *testing.T) {
	t.Parallel()

	w := newWorld()
	reconciled(t, w)

	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatal("reconcile raised no API")
	}
	method := methodOn(api, "/{proxy+}", anyMethod)
	if method == nil {
		t.Fatal("the API has no catch-all method")
	}
	if method.credentials != fakeInvokeRole {
		t.Errorf("the entry integration invokes as %q, want the %s output %q", method.credentials, bootstrap.OutputEdgeInvokeRoleARN, fakeInvokeRole)
	}
}

func TestCreateAPINamesTheQuota(t *testing.T) {
	t.Parallel()

	w := newWorld()
	e := bootstrapped(t, w)
	w.gateway.createErr = &agtypes.LimitExceededException{
		Message: aws.String("Maximum number of Regional APIs has been reached"),
	}

	_, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
	if err == nil {
		t.Fatal("Reconcile succeeded, want the quota surfaced")
	}
	for _, want := range []string{"Regional APIs per Region per account", "Service Quotas", "Amazon API Gateway"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
}

func TestCreateAPITellsAThrottleFromTheQuota(t *testing.T) {
	t.Parallel()

	for name, refusal := range map[string]error{
		"too many requests": &agtypes.TooManyRequestsException{Message: aws.String("Too Many Requests")},
		"rate exceeded":     &agtypes.LimitExceededException{Message: aws.String("Rate exceeded")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			e := bootstrapped(t, w)
			w.gateway.createErr = refusal

			_, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
			if err == nil {
				t.Fatal("Reconcile succeeded, want the throttle surfaced")
			}
			if strings.Contains(err.Error(), "Service Quotas") {
				t.Errorf("error = %v, want a throttle told apart from the account's API ceiling", err)
			}
			if !strings.Contains(err.Error(), "re-run the deploy") {
				t.Errorf("error = %v, want it to name retrying as the way out", err)
			}
		})
	}
}

func TestBindDomainPublishesAFrontPerHost(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	hosts := []string{"shop.example.com", "www.example.com"}
	for _, host := range hosts {
		if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
			t.Fatalf("BindDomain(%s): %v", host, err)
		}
	}

	records, err := edge.RecordsFor(edge.TargetFor(e, stack.State()), hosts)
	if err != nil {
		t.Fatalf("RecordsFor(%v): %v", hosts, err)
	}
	want := []edge.Record{
		{Name: hosts[0], Type: edge.RecordTypeCNAME, Value: regionalFront(hosts[0])},
		{Name: hosts[1], Type: edge.RecordTypeCNAME, Value: regionalFront(hosts[1])},
	}
	if !slices.Equal(records, want) {
		t.Errorf("records = %v, want %v: each API Gateway domain name has its own regional domain name", records, want)
	}

	if err := stack.UnbindDomain(ctx, hosts[0]); err != nil {
		t.Fatalf("UnbindDomain(%s): %v", hosts[0], err)
	}
	if _, err := edge.RecordsFor(edge.TargetFor(e, stack.State()), hosts[:1]); err == nil {
		t.Errorf("RecordsFor(%v) err = nil, want a refusal: the host is unbound and its front is gone", hosts[:1])
	}
}

func TestReconcileRecoversTheFrontOfADomainBoundBeforeItWasRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	const host = "shop.example.com"
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	forgotten := stack.State()
	forgotten.PublishFront(host, "")

	settled, err := e.Reconcile(ctx, testSpec(), forgotten)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	records, err := edge.RecordsFor(edge.TargetFor(e, settled.State()), []string{host})
	if err != nil {
		t.Fatalf("RecordsFor after a reconcile of state predating the front: %v", err)
	}
	if want := regionalFront(host); len(records) != 1 || records[0].Value != want {
		t.Errorf("records = %v, want a CNAME to %q read back from the domain name API Gateway already holds", records, want)
	}
}

func TestReconcileForgetsABindingWhoseDomainNameIsGone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	stack, err := e.Reconcile(ctx, testSpec(), edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	const host = "shop.example.com"
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}
	bound := stack.State()
	bound.PublishFront(host, "")
	delete(w.gateway.domains, host)

	spec := testSpec()
	var warned []string
	spec.Warn = func(m string) { warned = append(warned, m) }
	settled, err := e.Reconcile(ctx, spec, bound)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	if hosts := settled.State().Bound; slices.Contains(hosts, host) {
		t.Errorf("bound domains = %v, want %s forgotten: API Gateway holds no domain name for it any more", hosts, host)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], host) {
		t.Errorf("warned = %v, want the vanished domain name named", warned)
	}
}

func TestALedgerOpenedOnStateThatNamesNoTableRefuses(t *testing.T) {
	ctx := context.Background()
	stack, err := newWorld().edge().Open(edge.StackState{Slug: conformanceSlug, Class: edge.ClassProduction})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := stack.Ledger().History(ctx, ""); !errors.Is(err, edge.ErrStoreAbsent) {
		t.Errorf("History err = %v, want %v: a ledger over no table must refuse, not read an empty history", err, edge.ErrStoreAbsent)
	}
	if err := stack.Ledger().PutStaged(ctx, edge.DeploymentRecord{App: "web", Identity: "d1.f1"}); !errors.Is(err, edge.ErrStoreAbsent) {
		t.Errorf("PutStaged err = %v, want %v", err, edge.ErrStoreAbsent)
	}
}
