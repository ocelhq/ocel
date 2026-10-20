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
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/notfoundapi"
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

	if newWorld().edge().Facts().RunsCode {
		t.Error("the api-gateway edge says it runs code, but it declares only streaming; nothing of the app's code runs at this edge")
	}
}

func TestTheEdgeKindNeedsNoBootstrapPart(t *testing.T) {
	t.Parallel()

	if got := bootstrapplan.PartFrontingEdge(account.Parts(), Kind); got != "" {
		t.Errorf("bootstrapping with the %q edge raises the %q part, want none: its invoke role is in the base and its not-found API is the %s part", Kind, got, notfoundapi.Part)
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

func TestAPINamesCannotCollideAcrossSlugsAndPointers(t *testing.T) {
	t.Parallel()

	slugged := apiName(defaultNamespace, "shop-pr1", environment.TierProduction, "")
	pointed := apiName(defaultNamespace, "shop", environment.TierProduction, "pr1")
	if slugged == pointed {
		t.Errorf("apiName is %q for both a slug and a pointer that end the same; two projects would share one API", slugged)
	}
	if got := apiName(defaultNamespace, "shop", environment.TierProduction, ""); got != "ocel--shop--prod" {
		t.Errorf("apiName = %q, want the project stem the rest of the deploy path matches on", got)
	}
	if name := notfoundapi.NameFor(string(defaultNamespace), environment.TierProduction); strings.Contains(name, "--") {
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
	if owner != e.ProjectOwner(conformanceSlug, environment.TierProduction) {
		t.Errorf("DomainOwner = %q, which %q is not recognised as owning; preflight would report the project's own host as claimed by someone else", owner, conformanceSlug)
	}
	if owner == e.ProjectOwner("other", environment.TierProduction) {
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
	if out.ReleasesStore != nil || out.ClientCertificate != nil {
		t.Errorf("Bootstrap offered %+v, want nothing; the api-gateway edge hosts no store the bootstrap adopts", out)
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
		w.cfn.noInvokeRole = true
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
		t.Errorf("the entry integration invokes as %q, want the %s output %q", method.credentials, account.OutputEdgeInvokeRoleARN, fakeInvokeRole)
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
