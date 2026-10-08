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

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const (
	conformanceSlug = "conformance"

	entryFunction = "conformance-prod-web-r1234abcd"
)

func testSpec() edge.StackSpec {
	return edge.StackSpec{Version: "v1", Tier: environment.TierProduction, Slug: conformanceSlug}
}

func productionAPIName() string {
	return apiName(defaultNamespace, conformanceSlug, environment.TierProduction, "")
}

func bootstrapped(t *testing.T, w *world) *apiGateway {
	t.Helper()
	e := w.edge()
	if _, err := e.Bootstrap(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return e
}

func TestTheAPIGatewayEdgeBehavesAsEveryEdgeMust(t *testing.T) {
	edgeconformance.Run(t, edgeconformance.Suite{
		New: func(t *testing.T) (edge.Edge, edge.StackSpec) {
			return bootstrapped(t, newWorld()), testSpec()
		},
		Hostname: "shop.example.com",
		Previews: func(t *testing.T) (edge.Edge, edge.StackSpec, edge.PreviewWildcardSpec) {
			w := newWorld()
			e := w.edge()
			if _, err := e.Bootstrap(context.Background(), environment.TierPreview); err != nil {
				t.Fatalf("Bootstrap(preview): %v", err)
			}
			return e, previewStackSpec(), previewWildcardSpec()
		},
	})
}

func TestTheAPIGatewayRouterBehavesAsEveryRouterMust(t *testing.T) {
	fixture := func(w *world, e *apiGateway, stack edge.EdgeStack) routerconformance.Fixture {
		state := stack.State()
		return routerconformance.Fixture{
			Router: Router{p: e},
			Spec:   router.StackSpec{Tier: state.Tier, Slug: state.Slug},
			Prior:  router.NewStackState(state),
			Serving: func(pointer string) string {
				name := apiName(e.ns, state.Slug, state.Tier, pointer)
				w.gateway.mu.Lock()
				defer w.gateway.mu.Unlock()
				for _, api := range w.gateway.apis {
					if api.name == name && api.variables[entryVariable] != unsetVariable {
						return strings.TrimPrefix(api.variables[entryVariable], "fn-")
					}
				}
				return ""
			},
			FailNextPointerMove: func(err error) {
				w.gateway.mu.Lock()
				defer w.gateway.mu.Unlock()
				w.gateway.stageErr = err
			},
		}
	}
	previews := func(t *testing.T) routerconformance.Fixture {
		w := newWorld()
		e, stack := previewing(t, w)
		return fixture(w, e, stack)
	}
	record := func(app, build string) router.ReleaseRecord {
		return router.ReleaseRecord{App: app, Release: build, RootFunction: "/", RootFunctionPhysical: "fn-" + build}
	}
	t.Run("on a preview pointer", func(t *testing.T) {
		routerconformance.Run(t, routerconformance.Suite{
			New:      previews,
			Previews: previews,
			Pointer:  previewPoint,
			Hostname: "shop.example.com",
			Record:   record,
		})
	})
	t.Run("on the production pointer", func(t *testing.T) {
		routerconformance.Run(t, routerconformance.Suite{
			New: func(t *testing.T) routerconformance.Fixture {
				w := newWorld()
				e := bootstrapped(t, w)
				stack, err := e.Reconcile(context.Background(), testSpec(), edge.StackState{})
				if err != nil {
					t.Fatalf("Reconcile: %v", err)
				}
				return fixture(w, e, stack)
			},
			Hostname: "shop.example.com",
			Record:   record,
		})
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

func staged(t *testing.T, stack edge.EdgeStack, function, assets string) router.ReleaseRecord {
	t.Helper()
	record := router.ReleaseRecord{
		App:                  "web",
		Release:              "d1.f1",
		RootFunction:         "/",
		RootFunctionPhysical: function,
		AssetPrefix:          assets,
	}
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	return record
}

func methodOn(api *fakeAPI, path, httpMethod string) *fakeMethod {
	for id, resourcePath := range api.resources {
		if resourcePath == path {
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

func TestTheAPIGatewayEdgeRunsNoCode(t *testing.T) {
	t.Parallel()

	if !newWorld().edge().Facts().Compatibility.IsZero() {
		t.Error("the api-gateway edge names a compatibility, but it declares only streaming; nothing of the app's code runs at this edge")
	}
}

func TestTheEdgeKindReachesItsBootstrapFeature(t *testing.T) {
	t.Parallel()

	if got := bootstrapplan.FeatureNeedingEdge(bootstrap.Catalogue(), Kind); got != bootstrap.FeatureAPIGatewayEdge {
		t.Errorf("bootstrapping with the %q edge raises the %q feature, want %q; nothing else provisions the invoke role and the not-found API this edge fronts deployments with", Kind, got, bootstrap.FeatureAPIGatewayEdge)
	}
}

func TestReconcileShapesTheProductionAPI(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)

	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatalf("no REST API named for the stack; gateway has %v", w.gateway.mutations())
	}
	if ownState(t, stack).API != api.id {
		t.Errorf("state records API %q, want the one reconcile created (%q)", ownState(t, stack).API, api.id)
	}
	assertSet(t, "resources", slices.Collect(maps.Values(api.resources)), []string{
		"/", "/{proxy+}", "/.well-known", "/.well-known/ocel-edge",
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
		t.Errorf("catch-all response transfer mode = %q, want STREAM; the root function answers as a stream", entry.transfer)
	}
	if entry.timeoutMillis != 60_000 {
		t.Errorf("catch-all integration timeout = %dms, want 60000ms, the minute the root function has to answer", entry.timeoutMillis)
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
}

func promotedWithStatic(t *testing.T, w *world, prefixes ...string) (*fakeAPI, edge.EdgeStack) {
	t.Helper()
	stack := reconciled(t, w)
	record := router.ReleaseRecord{
		App:                  "web",
		Release:              "d1.f1",
		RootFunction:         "/",
		RootFunctionPhysical: entryFunction,
		AssetPrefix:          "assets/one",
		Static:               &edge.Static{ImmutablePrefixes: prefixes},
	}
	promote(t, stack, record, "p1", 1)
	return w.gateway.named(productionAPIName()), stack
}

func promote(t *testing.T, stack edge.EdgeStack, record router.ReleaseRecord, promotion string, ts int64) {
	t.Helper()
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	move := router.PointerMove{Promotion: router.Promotion{PromotionID: promotion, Ts: ts, Releases: map[string]string{record.App: record.Release}}}
	if err := openRouter(stack).MovePointer(context.Background(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer: %v", err)
	}
}

func TestAPromotionRoutesEachImmutablePrefixTheReleaseStatesToTheAssetBucket(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, _ := promotedWithStatic(t, w, "/docs/_app/immutable/")

	static := methodOn(api, "/docs/_app/immutable/{proxy+}", getMethod)
	if static == nil {
		t.Fatalf("no static-asset method; resources are %v", slices.Sorted(maps.Values(api.resources)))
	}
	if static.integration != agtypes.IntegrationTypeAws {
		t.Errorf("static integration = %q, want the S3 service integration", static.integration)
	}
	if static.transfer == agtypes.ResponseTransferModeStream {
		t.Error("static integration streams, want it buffered so API Gateway maps its response headers")
	}
	if want := "s3:path/" + fakeAssetBucket + "/${stageVariables." + assetsVariable + "}/docs/_app/immutable/{proxy}"; !strings.Contains(static.uri, want) {
		t.Errorf("static URI = %q, want it to hold %q", static.uri, want)
	}
	if methodOn(api, "/_next/static/{proxy+}", getMethod) != nil {
		t.Error("a route sends /_next/static/ to the bucket, a prefix this release never stated")
	}
}

func TestAPromotionRemovesTheStaticRouteOfAPrefixTheReleaseNoLongerStates(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, stack := promotedWithStatic(t, w, "/_next/static/")
	if methodOn(api, "/_next/static/{proxy+}", getMethod) == nil {
		t.Fatal("the first promotion routed no /_next/static/; the removal this test covers cannot happen")
	}
	deployments := w.gateway.count("CreateDeployment " + api.name)

	promote(t, stack, router.ReleaseRecord{
		App: "web", Release: "d2.f1", RootFunction: "/", RootFunctionPhysical: entryFunction, AssetPrefix: "assets/two",
		Static: &edge.Static{ImmutablePrefixes: []string{"/_app/immutable/"}},
	}, "p2", 2)

	if methodOn(api, "/_next/static/{proxy+}", getMethod) != nil {
		t.Error("/_next/static/ still reaches the bucket under the release that no longer states it")
	}
	if methodOn(api, "/_app/immutable/{proxy+}", getMethod) == nil {
		t.Error("/_app/immutable/ reaches no bucket")
	}
	if got := w.gateway.count("CreateDeployment " + api.name); got != deployments+1 {
		t.Errorf("deployments = %d, want one more than %d: the stage serves the routes it was last deployed with", got, deployments)
	}
}

func TestAPromotionOfTheSameStaticRoutesPublishesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, stack := promotedWithStatic(t, w, "/_app/immutable/")
	deployments := w.gateway.count("CreateDeployment " + api.name)

	promote(t, stack, router.ReleaseRecord{
		App: "web", Release: "d2.f1", RootFunction: "/", RootFunctionPhysical: entryFunction, AssetPrefix: "assets/two",
		Static: &edge.Static{ImmutablePrefixes: []string{"/_app/immutable/"}},
	}, "p2", 2)

	if got := w.gateway.count("CreateDeployment " + api.name); got != deployments {
		t.Errorf("deployments = %d, want %d: a promotion that changes no route needs only its stage variables", got, deployments)
	}
}

func TestAPrefixNoResourcePathCanSpellIsLeftToTheRootFunction(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, _ := promotedWithStatic(t, w, "/caf\u00e9/_app/immutable/")

	assertSet(t, "resources", slices.Collect(maps.Values(api.resources)), []string{
		"/", "/{proxy+}", "/.well-known", "/.well-known/ocel-edge",
	})
}

func TestOnlyTheRoutesThatCanSetTheRouterHeaderDeclareIt(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, _ := promotedWithStatic(t, w, "/_next/static/")
	for _, path := range []string{"/", "/{proxy+}"} {
		entry := methodOn(api, path, anyMethod)
		if len(entry.methodResponses) != 0 {
			t.Errorf("the entry method on %s declares the response %v; API Gateway ignores method responses on a proxy integration, so the root function is what sets %s", path, slices.Sorted(maps.Keys(entry.methodResponses)), router.HeaderRouter)
		}
	}
	static := methodOn(api, "/_next/static/{proxy+}", getMethod)
	if got := static.integrationResponse["200"][routerHeaderParameter]; got != "'"+string(Kind)+"'" {
		t.Errorf("static integration response sets %s = %q, want 'api-gateway'", router.HeaderRouter, got)
	}
}

func TestTheLivenessProbePathIsAnsweredByTheGatewayItselfNamingItsRouter(t *testing.T) {
	t.Parallel()

	w := newWorld()
	reconciled(t, w)

	api := w.gateway.named(productionAPIName())
	if api == nil {
		t.Fatal("no REST API for the stack")
	}
	probed := methodOn(api, edge.LivenessProbePath, getMethod)
	if probed == nil {
		t.Fatalf("no method on %s; methods are %v. An app behind the proxy integration answers every other path without %s, so a probe that reads the marker never sees this edge serve", edge.LivenessProbePath, slices.Sorted(maps.Keys(api.methods)), router.HeaderRouter)
	}
	if probed.integration != agtypes.IntegrationTypeMock {
		t.Errorf("the probe path's integration = %q, want MOCK: the gateway answers it, not the app", probed.integration)
	}
	if !probed.methodResponses["200"][routerHeaderParameter] {
		t.Errorf("the probe path's 200 declares %v, want %s", probed.methodResponses["200"], router.HeaderRouter)
	}
	if got := probed.integrationResponse["200"][routerHeaderParameter]; got != "'"+string(Kind)+"'" {
		t.Errorf("the probe path's integration response sets %s = %q, want 'api-gateway'", router.HeaderRouter, got)
	}
}

func TestPromoteRefusesAContainerReleaseByName(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := router.ReleaseRecord{App: "web", Release: "d1.f1", Image: "ocel/web:sha256-abc", Origin: "https://abc.eu-west-1.awsapprunner.com"}
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "container") || !strings.Contains(err.Error(), "cloudfront") {
		t.Fatalf("Promote of a container release = %v, want it refused with the edge that can front one: this edge invokes a function, and a container has none", err)
	}
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none: a refused promotion moves nothing", got)
	}
}

func TestPromoteMovesTheStageOnce(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "assets/shop/web/r1234abcd")

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if got := w.gateway.count("UpdateStage"); got != 1 {
		t.Errorf("UpdateStage calls = %d, want exactly one; promoting is moving one stage", got)
	}
	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != record.RootFunctionPhysical {
		t.Errorf("stage variable %s = %q, want the root function %q", entryVariable, api.variables[entryVariable], record.RootFunctionPhysical)
	}
	if api.variables[assetsVariable] != record.AssetPrefix {
		t.Errorf("stage variable %s = %q, want %q", assetsVariable, api.variables[assetsVariable], record.AssetPrefix)
	}
	history, err := openRouter(stack).Ledger.History(context.Background(), "")
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
	record := router.ReleaseRecord{
		App:                  "web",
		Release:              "d1.f1",
		RootFunction:         "/",
		RootFunctionPhysical: entryFunction,
	}
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	if err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
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

		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "d1.f1"}}}, progress.Discard())
		if err == nil {
			t.Fatal("Promote succeeded against a promotion nothing was staged for")
		}
		assertStageUnmoved(t, w)
	})

	t.Run("a record from before the root function was recorded", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		record := staged(t, stack, "", "assets/one")

		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}}, progress.Discard())
		if err == nil {
			t.Fatal("Promote succeeded on a record naming no root function")
		}
		if !strings.Contains(err.Error(), "root function") {
			t.Errorf("error = %v, want it to name what the record is missing", err)
		}
		assertUnserved(t, err)
		assertStageUnmoved(t, w)
	})

	t.Run("more apps than one stage can serve", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		stack := reconciled(t, w)
		for _, app := range []string{"api", "web"} {
			record := router.ReleaseRecord{App: app, Release: "d1.f1", RootFunction: "/", RootFunctionPhysical: "conformance-prod-" + app + "-r1234abcd"}
			if err := openRouter(stack).Ledger.PutStaged(ctx, record); err != nil {
				t.Fatalf("PutStaged(%s): %v", app, err)
			}
		}

		promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"api": "d1.f1", "web": "d1.f1"}}
		err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: promotion}, progress.Discard())
		if err == nil {
			t.Fatal("Promote succeeded on two apps, want the one-app limit surfaced rather than one app silently served")
		}
		for _, want := range []string{"api", "web", "cloudflare"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
		assertUnserved(t, err)
		assertStageUnmoved(t, w)
	})
}

func assertStageUnmoved(t *testing.T, w *world) {
	t.Helper()
	if got := w.gateway.count("UpdateStage"); got != 0 {
		t.Errorf("UpdateStage calls = %d, want none behind a promotion the edge refused", got)
	}
	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != unsetVariable {
		t.Errorf("stage variable %s = %q, want the stage left where it was", entryVariable, api.variables[entryVariable])
	}
}

func assertUnserved(t *testing.T, err error) {
	t.Helper()
	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Errorf("MovePointer = %v, want router.Unserved: the stage never moved, so the ledger must take the promotion back", err)
	}
}

func TestAPointerMoveTheStageRefusesIsUnserved(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "assets/shop/web/r1234abcd")
	w.gateway.stageErr = errors.New("stage is being updated by another operation")

	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}
	err := openRouter(stack).MovePointer(context.Background(), router.PointerMove{Promotion: promotion}, progress.Discard())
	if err == nil {
		t.Fatal("Promote succeeded, want the stage failure surfaced")
	}
	assertUnserved(t, err)
}

func TestRollbackMovesTheStageOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	first := router.ReleaseRecord{App: "web", Release: "d1.f1", RootFunction: "/", RootFunctionPhysical: "conformance-prod-web-r1111aaaa", AssetPrefix: "assets/one"}
	second := router.ReleaseRecord{App: "web", Release: "d2.f2", RootFunction: "/", RootFunctionPhysical: "conformance-prod-web-r2222bbbb", AssetPrefix: "assets/two"}
	for _, record := range []router.ReleaseRecord{first, second} {
		if err := openRouter(stack).Ledger.PutStaged(ctx, record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": first.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": second.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p2): %v", err)
	}

	before := w.gateway.count("UpdateStage")
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p3", Ts: 3, Releases: map[string]string{"web": first.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("rollback to p1's build: %v", err)
	}
	if got := w.gateway.count("UpdateStage") - before; got != 1 {
		t.Errorf("UpdateStage calls for the rollback = %d, want exactly one", got)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != first.RootFunctionPhysical {
		t.Errorf("stage variable %s = %q, want the rolled-back release %q", entryVariable, api.variables[entryVariable], first.RootFunctionPhysical)
	}
	history, err := openRouter(stack).Ledger.History(ctx, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 3 || history[0].PromotionID != "p3" || !history[0].Active || history[0].Releases["web"] != first.Release {
		t.Errorf("history = %v, want p3 in effect with p1's build over the two promotions before it", history)
	}
}

func TestARollbackTheStageRefusesIsUnserved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	record := staged(t, stack, entryFunction, "assets/one")
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}
	w.gateway.stageErr = errors.New("stage is being updated by another operation")

	err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": record.Release}}}, progress.Discard())
	if err == nil {
		t.Fatal("rollback succeeded, want the stage failure surfaced")
	}
	assertUnserved(t, err)
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
		"/", "/{proxy+}", "/.well-known", "/.well-known/ocel-edge",
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
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if _, err := e.Reconcile(ctx, testSpec(), stack.State()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	api := w.gateway.named(productionAPIName())
	if api.variables[entryVariable] != record.RootFunctionPhysical {
		t.Errorf("stage variable %s = %q, want the release in effect left alone by a reconcile", entryVariable, api.variables[entryVariable])
	}
}

func TestReconcileLeavesTheMethodsItAlreadyOpened(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	e := bootstrapped(t, w)
	if _, err := e.Reconcile(ctx, testSpec(), edge.StackState{}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	opened := w.gateway.count("PutMethod")
	if opened == 0 {
		t.Fatal("the first reconcile opened no method; the repeat this test covers cannot happen")
	}
	integrated := w.gateway.count("PutIntegration")
	for _, call := range []string{"PutMethodResponse", "PutIntegrationResponse"} {
		if w.gateway.count(call) == 0 {
			t.Fatalf("the first reconcile made no %s; the repeat this test covers cannot happen", call)
		}
	}
	w.gateway.calls = nil

	if _, err := e.Reconcile(ctx, testSpec(), edge.StackState{}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := w.gateway.count("PutMethod"); got != 0 {
		t.Errorf("PutMethod calls on the second reconcile = %d, want none; API Gateway rejects a method the resource already has", got)
	}
	if got := w.gateway.count("PutMethodResponse"); got != 0 {
		t.Errorf("PutMethodResponse calls on the second reconcile = %d, want none; API Gateway rejects a status-code response the method already has", got)
	}
	if got := w.gateway.count("PutIntegrationResponse"); got != 0 {
		t.Errorf("PutIntegrationResponse calls on the second reconcile = %d, want none; API Gateway rejects a status-code response the integration already has", got)
	}
	if got := w.gateway.count("PutIntegration"); got != integrated {
		t.Errorf("PutIntegration calls on the second reconcile = %d, want the %d the first made; skipping the method must not skip what it points at", got, integrated)
	}
}

func TestAPromotionRewritesStaticResponseHeadersThatNoLongerMatchThePlan(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, stack := promotedWithStatic(t, w, "/_next/static/")
	resource := api.id + "/_next/static/{proxy+}"
	method := api.methods[resource+" "+getMethod]
	if method == nil {
		t.Fatalf("the static-asset method is missing from %s; the drift this test covers cannot happen", api.id)
	}
	method.methodResponses["200"] = map[string]bool{routerHeaderParameter: true}
	method.integrationResponse["200"] = map[string]string{routerHeaderParameter: "'stale'"}

	promote(t, stack, router.ReleaseRecord{
		App: "web", Release: "d2.f1", RootFunction: "/", RootFunctionPhysical: entryFunction, AssetPrefix: "assets/two",
		Static: &edge.Static{ImmutablePrefixes: []string{"/_next/static/"}},
	}, "p2", 2)

	if got := method.methodResponses["200"]["method.response.header.Content-Type"]; !got {
		t.Errorf("the method response still declares %v, not the headers the plan names; a header dropped outside Ocel would stay dropped", method.methodResponses["200"])
	}
	if got := method.integrationResponse["200"][routerHeaderParameter]; got != "'"+routerHeaderValue+"'" {
		t.Errorf("the integration response maps %s to %q, want %q", routerHeaderParameter, got, "'"+routerHeaderValue+"'")
	}
}

func TestAPINamesCannotCollideAcrossSlugsAndPointers(t *testing.T) {
	t.Parallel()

	slugged := apiName(defaultNamespace, "shop-pr1", environment.TierProduction, "")
	pointed := apiName(defaultNamespace, "shop", environment.TierProduction, "pr1")
	if slugged == pointed {
		t.Errorf("apiName is %q for both a slug and a pointer that end the same; two projects would share one API", slugged)
	}
	if got := apiName(defaultNamespace, "shop", environment.TierProduction, ""); got != "ocel--shop--production" {
		t.Errorf("apiName = %q, want the project stem the rest of the deploy path matches on", got)
	}
	if name := defaultNamespace.EdgeNotFoundAPIName(environment.TierProduction); cloudflare.ProjectOwnsWorker(string(defaultNamespace), "not", name) || strings.Contains(name, "--") {
		t.Errorf("the not-found API is named %q, which a project could claim as its own", name)
	}
}

func TestDomainOwnerNamesTheProjectThatOwnsTheHost(t *testing.T) {
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
	if !cloudflare.ProjectOwnsWorker(string(defaultNamespace), conformanceSlug, owner) {
		t.Errorf("DomainOwner = %q, which %q is not recognised as owning; preflight would report the project's own host as claimed by someone else", owner, conformanceSlug)
	}
	if cloudflare.ProjectOwnsWorker(string(defaultNamespace), "other", owner) {
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

func TestTeardownLeavesTheCoreStacksResourcesToCloudFormation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			e := w.edge()
			if _, err := e.Bootstrap(ctx, tier); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			w.gateway.calls = nil

			if err := e.Teardown(ctx, tier); err != nil {
				t.Fatalf("Teardown: %v", err)
			}
			if got := w.gateway.mutations(); len(got) != 0 {
				t.Errorf("teardown called %v, want nothing; the role and the 404 responder go down with the core stack", got)
			}
		})
	}

	t.Run("an unknown tier is refused rather than reported done", func(t *testing.T) {
		t.Parallel()

		w := newWorld()
		if err := w.edge().Teardown(ctx, environment.Tier("staging")); err == nil {
			t.Fatal("Teardown(staging) reported success having removed nothing")
		}
		if len(w.gateway.calls) != 0 {
			t.Errorf("an unknown tier called %v, want nothing", w.gateway.calls)
		}
	})
}

func TestBootstrapWritesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	out, err := w.edge().Bootstrap(ctx, environment.TierProduction)
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
	forgotten.PublishAddress(host, "")

	rerun, err := e.Reconcile(ctx, testSpec(), forgotten)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	records, err := edge.RecordsFor(edge.TargetFor(e, rerun.State()), []string{host})
	if err != nil {
		t.Fatalf("RecordsFor after a reconcile of state predating the front: %v", err)
	}
	if want := regionalFront(host); len(records) != 1 || records[0].Value != want {
		t.Errorf("records = %v, want a CNAME to %q read back from the domain name API Gateway already has", records, want)
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
	bound.PublishAddress(host, "")
	delete(w.gateway.domains, host)

	spec := testSpec()
	var warned []string
	spec.Warn = func(m string) { warned = append(warned, m) }
	rerun, err := e.Reconcile(ctx, spec, bound)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}

	if hosts := rerun.State().Bound; slices.Contains(hosts, host) {
		t.Errorf("bound domains = %v, want %s forgotten: API Gateway has no domain name for it any more", hosts, host)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], host) {
		t.Errorf("warned = %v, want the vanished domain name named", warned)
	}
}

func TestALedgerOpenedOnStateThatNamesNoTableRefuses(t *testing.T) {
	ctx := context.Background()
	stack, err := newWorld().edge().Open(edge.StackState{Slug: conformanceSlug, Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := openRouter(stack).Ledger.History(ctx, ""); !errors.Is(err, edge.ErrStoreAbsent) {
		t.Errorf("History err = %v, want %v: a ledger over no table must refuse, not read an empty history", err, edge.ErrStoreAbsent)
	}
	if err := openRouter(stack).Ledger.PutStaged(ctx, router.ReleaseRecord{App: "web", Release: "d1.f1"}); !errors.Is(err, edge.ErrStoreAbsent) {
		t.Errorf("PutStaged err = %v, want %v", err, edge.ErrStoreAbsent)
	}
}

func TestBindDomainAfterAPromotionMapsTheHostOntoThePromotedStage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newWorld()
	stack := reconciled(t, w)
	staged(t, stack, entryFunction, "")
	promotion := router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": "d1.f1"}}
	if err := openRouter(stack).MovePointer(ctx, router.PointerMove{Promotion: promotion}, progress.Discard()); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	host := "shop.example.com"
	if err := stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: "arn:aws:acm:eu-west-1:123456789012:certificate/abc"}); err != nil {
		t.Fatalf("BindDomain: %v", err)
	}

	api := w.gateway.named(productionAPIName())
	mapping, mapped := w.gateway.domains[host].mappings["(none)"]
	if !mapped || aws.ToString(mapping.RestApiId) != api.id || aws.ToString(mapping.Stage) != stageName {
		t.Fatalf("%s maps onto %+v, want the %s stage of %s: a hostname bound after the promotion serves it without another deploy", host, mapping, stageName, api.id)
	}
	if api.variables[entryVariable] != entryFunction {
		t.Errorf("the stage %s is mapped onto serves %q, want the promoted %q", host, api.variables[entryVariable], entryFunction)
	}
}
