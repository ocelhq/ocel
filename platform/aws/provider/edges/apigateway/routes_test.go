package apigateway

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

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

func TestEveryResourceAStaticRouteNeedsSendsTheRestOfItsPathsToTheRootFunction(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, _ := promotedWithStatic(t, w, "/docs/_app/immutable/")

	for _, path := range []string{"/docs", "/docs/{proxy+}", "/docs/_app", "/docs/_app/{proxy+}", "/docs/_app/immutable"} {
		entry := methodOn(api, path, anyMethod)
		if entry == nil {
			t.Errorf("%s has no method, so API Gateway answers every request it matches, such as /docs/_app/version.json, itself instead of the root function", path)
			continue
		}
		if !strings.Contains(entry.uri, "function:${stageVariables."+entryVariable+"}") {
			t.Errorf("%s integrates with %q, want the root function", path, entry.uri)
		}
	}
}

func TestAPromotionRemovesEveryResourceOnlyAPrefixTheReleaseNoLongerStatesNeeded(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, stack := promotedWithStatic(t, w, "/docs/_next/static/")

	promote(t, stack, router.ReleaseRecord{
		App: "web", Release: "d2.f1", RootFunction: "/", RootFunctionPhysical: entryFunction, AssetPrefix: "assets/two",
		Static: &edge.Static{ImmutablePrefixes: []string{"/_app/immutable/"}},
	}, "p2", 2)

	assertSet(t, "resources", slices.Collect(maps.Values(api.resources)), []string{
		"/", "/{proxy+}", "/.well-known", "/.well-known/ocel-edge",
		"/_app", "/_app/{proxy+}", "/_app/immutable", "/_app/immutable/{proxy+}",
	})
}

func TestAPromotionPublishesTheStaticRoutesAPromotionThatFailedAfterShapingThemNeverPublished(t *testing.T) {
	t.Parallel()

	w := newWorld()
	stack := reconciled(t, w)
	api := w.gateway.named(productionAPIName())
	record := router.ReleaseRecord{
		App: "web", Release: "d1.f1", RootFunction: "/", RootFunctionPhysical: entryFunction, AssetPrefix: "assets/one",
		Static: &edge.Static{ImmutablePrefixes: []string{"/_app/immutable/"}},
	}
	if err := openRouter(stack).Ledger.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged: %v", err)
	}
	w.gateway.deploymentErr = errors.New("throttled")
	move := router.PointerMove{Promotion: router.Promotion{PromotionID: "p1", Ts: 1, Releases: map[string]string{"web": record.Release}}}
	if err := openRouter(stack).MovePointer(context.Background(), move, progress.Discard()); err == nil {
		t.Fatal("MovePointer succeeded with every deployment failing; the retry this test covers cannot happen")
	}
	w.gateway.deploymentErr = nil
	deployments := w.gateway.count("CreateDeployment " + api.name)

	retry := router.PointerMove{Promotion: router.Promotion{PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": record.Release}}}
	if err := openRouter(stack).MovePointer(context.Background(), retry, progress.Discard()); err != nil {
		t.Fatalf("retried MovePointer: %v", err)
	}
	if got := w.gateway.count("CreateDeployment " + api.name); got != deployments+1 {
		t.Errorf("deployments = %d, want one more than %d: the routes the failed promotion shaped were never published", got, deployments)
	}
}

func TestAPromotionSendsTheGetRequestsOfAResourceThatStoppedServingAPrefixToTheRootFunction(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, stack := promotedWithStatic(t, w, "/a/")
	if methodOn(api, "/a/{proxy+}", getMethod) == nil {
		t.Fatal("the first promotion routed no /a/; the change this test covers cannot happen")
	}
	deployments := w.gateway.count("CreateDeployment " + api.name)

	promote(t, stack, router.ReleaseRecord{
		App: "web", Release: "d2.f1", RootFunction: "/", RootFunctionPhysical: entryFunction, AssetPrefix: "assets/two",
		Static: &edge.Static{ImmutablePrefixes: []string{"/a/b/"}},
	}, "p2", 2)

	if methodOn(api, "/a/{proxy+}", getMethod) != nil {
		t.Error("GET /a/page still reaches the bucket under the release that states only /a/b/, instead of the root function")
	}
	if methodOn(api, "/a/b/{proxy+}", getMethod) == nil {
		t.Error("/a/b/ reaches no bucket")
	}
	if got := w.gateway.count("CreateDeployment " + api.name); got != deployments+1 {
		t.Errorf("deployments = %d, want one more than %d: the stage serves the routes it was last deployed with", got, deployments)
	}
}

func TestAPrefixNestedUnderAnotherTheReleaseStatesIsServedByTheOuterStaticRoute(t *testing.T) {
	t.Parallel()

	w := newWorld()
	api, _ := promotedWithStatic(t, w, "/a/b/", "/a/", "/a/")

	outer := methodOn(api, "/a/{proxy+}", getMethod)
	if outer == nil {
		t.Fatal("GET /a/ reaches no bucket: the nested /a/b/ took over the resource /a/ is served from")
	}
	if !strings.HasSuffix(outer.uri, "}/a/{proxy}") {
		t.Errorf("/a/{proxy+} integrates with %q, want the bucket under /a/", outer.uri)
	}
	for _, path := range slices.Collect(maps.Values(api.resources)) {
		if strings.HasPrefix(path, "/a/b") {
			t.Errorf("%s exists, but /a/b/ is under /a/, whose static route already serves it", path)
		}
	}
}
