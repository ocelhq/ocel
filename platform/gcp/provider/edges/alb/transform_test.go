package alb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albprogram"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

func patchedFront(tier environment.Tier, result transform.Result) albresources.LoadBalancerSpec {
	return albresources.LoadBalancerSpec{
		Namespace: "ocel",
		Names:     albresources.LoadBalancerNames("ocel", tier),
		Tier:      tier,
		Assets:    &tierAssets,
		Patches:   result,
	}
}

func TestAFrontPatchReachesTheResourceOfTheTiersLoadBalancerItNames(t *testing.T) {
	t.Parallel()

	patches := transform.Patches{}
	for _, key := range albresources.LoadBalancerSurfaces {
		patches[key] = map[string]any{"description": "patched " + key}
	}
	program, err := albprogram.LoadBalancer(patchedFront(environment.TierProduction, transform.Result{Patches: patches}))
	if err != nil {
		t.Fatalf("albprogram.LoadBalancer() = %v", err)
	}
	seen, err := declared(program)
	if err != nil {
		t.Fatalf("the frontend program = %v", err)
	}

	for key, name := range map[string]string{
		albresources.SurfaceAddress:         "ocel-prod-alb-address",
		albresources.SurfaceNotFoundBackend: "ocel-prod-alb-notfound",
		albresources.SurfaceAssetsBackend:   "ocel-prod-alb-assets",
		albresources.SurfaceCertificateMap:  "ocel-prod-alb-certs",
		albresources.SurfaceURLMap:          "ocel-prod-alb-routes",
		albresources.SurfaceHTTPSProxy:      "ocel-prod-alb-https",
		albresources.SurfaceForwardingRule:  "ocel-prod-alb-forward",
	} {
		if got := seen[name].Patch["description"]; got != "patched "+key {
			t.Errorf("%s carries %v, want the patch written under loadBalancer.%s", name, seen[name].Patch, key)
		}
	}
	if err := program.Patches.RefuseUnclaimed(); err != nil {
		t.Errorf("RefuseUnclaimed() = %v, want every surface to reach a resource the program declares", err)
	}
}

func TestAPatchOnAResourceTheFrontDidNotDeclareIsRefusedAfterTheRun(t *testing.T) {
	t.Parallel()

	spec := patchedFront(environment.TierProduction, transform.Result{Patches: transform.Patches{
		albresources.SurfaceAssetsBackend: {"compressionMode": "AUTOMATIC"},
	}})
	spec.Assets = nil
	program := must(albprogram.LoadBalancer(spec))
	if _, err := declared(program); err != nil {
		t.Fatal(err)
	}

	err := program.Patches.RefuseUnclaimed()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "assetsBackend") {
		t.Errorf("RefuseUnclaimed() = %v, want the assets backend patch refused by name: nothing it could reach was provisioned", err)
	}
}

func TestAPreviewAssetsBucketAddsAPatchsHeadersAfterItsNoindexHeader(t *testing.T) {
	t.Parallel()

	program := must(albprogram.LoadBalancer(patchedFront(environment.TierPreview, transform.Result{Patches: transform.Patches{
		albresources.SurfaceAssetsBackend: {"customResponseHeaders": []any{"Strict-Transport-Security: max-age=31536000"}},
	}})))
	seen, err := declared(program)
	if err != nil {
		t.Fatal(err)
	}

	bucket := seen["ocel-preview-alb-assets"]
	want := []any{"X-Robots-Tag: noindex", "Strict-Transport-Security: max-age=31536000"}
	if got := bucket.Args["customResponseHeaders"]; !reflect.DeepEqual(got, want) {
		t.Errorf("the preview assets bucket adds %v, want %v: a patch's headers join the noindex header rather than replace it", got, want)
	}
	if _, replaced := bucket.Patch["customResponseHeaders"]; replaced {
		t.Error("the patch still carries customResponseHeaders, which would replace the list ocel built")
	}
}

func TestAFrontRulesTagsLabelTheLoadBalancersLabelledResources(t *testing.T) {
	t.Parallel()

	seen, err := declared(albprogram.LoadBalancer(patchedFront(environment.TierProduction, transform.Result{Tags: map[string]string{"team": "web"}})))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"ocel-prod-alb-address", "ocel-prod-alb-certs", "ocel-prod-alb-forward"} {
		if got := seen[name].Args["labels"]; !reflect.DeepEqual(got, map[string]any{"team": "web"}) {
			t.Errorf("%s is labelled %v, want team=web from the rule's tags", name, got)
		}
	}
}

func TestAFrontWithNoTagsCarriesNoLabels(t *testing.T) {
	t.Parallel()

	seen, err := declared(albprogram.LoadBalancer(frontSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if got, labelled := seen["ocel-prod-alb-forward"].Args["labels"]; labelled {
		t.Errorf("the forwarding rule is labelled %v, want no labels: a load balancer raised before transforms must not change", got)
	}
}

func patchedHosts(tier environment.Tier) albresources.BindingSpec {
	return albresources.BindingSpec{
		Namespace:      "ocel",
		Region:         "europe-west1",
		Slug:           "shop",
		Tier:           tier,
		CertificateMap: "certs",
		Hosts: map[string]albresources.BindingHost{
			"shop.example.com":  {App: "web", Certificate: "cert", Service: "svc-web", Backend: must(albresources.BackendName("ocel", "shop", tier, "shop.example.com"))},
			"www.example.com":   {App: "web", Certificate: "cert", Service: "svc-web", Backend: must(albresources.BackendName("ocel", "shop", tier, "www.example.com"))},
			"admin.example.com": {App: "admin", Certificate: "cert", Service: "svc-admin", Backend: must(albresources.BackendName("ocel", "shop", tier, "admin.example.com"))},
		},
	}
}

func TestAnAppsEdgePatchReachesTheResourcesOfEveryHostnameItServesAndNoOther(t *testing.T) {
	t.Parallel()

	spec := patchedHosts(environment.TierProduction)
	spec.Patches = map[string]transform.Result{"web": {Patches: transform.Patches{
		albresources.SurfaceBackendService:      {"securityPolicy": "web-armor"},
		albresources.SurfaceNEG:                 {"description": "web's neg"},
		albresources.SurfaceCertificateMapEntry: {"description": "web's entry"},
	}}}
	program := must(albprogram.Binding(spec))
	seen, err := declared(program)
	if err != nil {
		t.Fatal(err)
	}

	for _, hostname := range []string{"shop.example.com", "www.example.com"} {
		if got := seen[must(albresources.BackendName("ocel", "shop", environment.TierProduction, hostname))].Patch["securityPolicy"]; got != "web-armor" {
			t.Errorf("%s's backend service carries %v, want web's security policy", hostname, got)
		}
		if got := seen[must(albresources.NegName("ocel", "shop", environment.TierProduction, hostname))].Patch["description"]; got != "web's neg" {
			t.Errorf("%s's neg carries %v, want web's patch", hostname, got)
		}
		if got := seen[must(albresources.EntryName("ocel", "shop", environment.TierProduction, hostname))].Patch["description"]; got != "web's entry" {
			t.Errorf("%s's certificate map entry carries %v, want web's patch", hostname, got)
		}
	}
	if got := seen[must(albresources.BackendName("ocel", "shop", environment.TierProduction, "admin.example.com"))].Patch; got != nil {
		t.Errorf("admin's backend service carries %v, want nothing: the patch was written for web", got)
	}
	if err := program.Patches.RefuseUnclaimed(); err != nil {
		t.Errorf("RefuseUnclaimed() = %v", err)
	}
}

func TestAPreviewBackendAddsAnAppsHeadersAfterItsNoindexHeader(t *testing.T) {
	t.Parallel()

	spec := patchedHosts(environment.TierPreview)
	spec.Patches = map[string]transform.Result{"web": {Patches: transform.Patches{
		albresources.SurfaceBackendService: {"customResponseHeaders": []any{"Strict-Transport-Security: max-age=31536000"}},
	}}}
	seen, err := declared(albprogram.Binding(spec))
	if err != nil {
		t.Fatal(err)
	}

	backend := seen[must(albresources.BackendName("ocel", "shop", environment.TierPreview, "shop.example.com"))]
	want := []any{"X-Robots-Tag: noindex", "Strict-Transport-Security: max-age=31536000"}
	if got := backend.Args["customResponseHeaders"]; !reflect.DeepEqual(got, want) {
		t.Errorf("web's preview backend adds %v, want %v", got, want)
	}
	admin := seen[must(albresources.BackendName("ocel", "shop", environment.TierPreview, "admin.example.com"))]
	if got := admin.Args["customResponseHeaders"]; !reflect.DeepEqual(got, []any{"X-Robots-Tag: noindex"}) {
		t.Errorf("admin's preview backend adds %v, want only the noindex header", got)
	}
}

func TestAnAppsTagsLabelItsCertificateMapEntries(t *testing.T) {
	t.Parallel()

	spec := patchedHosts(environment.TierProduction)
	spec.Patches = map[string]transform.Result{"web": {Tags: map[string]string{"team": "storefront"}}}
	seen, err := declared(albprogram.Binding(spec))
	if err != nil {
		t.Fatal(err)
	}

	if got := seen[must(albresources.EntryName("ocel", "shop", environment.TierProduction, "shop.example.com"))].Args["labels"]; !reflect.DeepEqual(got, map[string]any{"team": "storefront"}) {
		t.Errorf("web's entry is labelled %v, want team=storefront", got)
	}
	if got, labelled := seen[must(albresources.EntryName("ocel", "shop", environment.TierProduction, "admin.example.com"))].Args["labels"]; labelled {
		t.Errorf("admin's entry is labelled %v, want none", got)
	}
}

func TestAPatchGivingCustomResponseHeadersSomethingButHeadersIsRefused(t *testing.T) {
	t.Parallel()

	spec := patchedHosts(environment.TierProduction)
	spec.Patches = map[string]transform.Result{"web": {Patches: transform.Patches{
		albresources.SurfaceBackendService: {"customResponseHeaders": "X-Frame-Options: DENY"},
	}}}
	_, err := albprogram.Binding(spec)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "customResponseHeaders") {
		t.Errorf("albprogram.Binding() = %v, want the header patch refused by name", err)
	}
}

type evaluatingPass struct {
	asked  []transform.Request
	result transform.Result
}

func (p *evaluatingPass) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	p.asked = append(p.asked, req)
	return []transform.Result{p.result}, nil
}

func outputOf(kind, name, property string) map[string]any {
	return map[string]any{"$ocelOutput": map[string]any{"type": kind, "name": name, "property": property}}
}

func TestTheTiersLoadBalancerIsEvaluatedWithNoEnvironmentOrApp(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{albresources.SurfaceHTTPSProxy: {"sslPolicy": "modern-tls"}}}}
	result, err := EvaluateFront(context.Background(), pass, environment.TierProduction)
	if err != nil {
		t.Fatalf("EvaluateFront() = %v", err)
	}
	if got := result.Patches[albresources.SurfaceHTTPSProxy]["sslPolicy"]; got != "modern-tls" {
		t.Errorf("EvaluateFront() = %v, want the proxy's patch", result)
	}
	want := transform.Request{Provider: "gcp", EnvTier: "production", Resources: []transform.Resource{{Type: albresources.TransformKindLoadBalancer, Name: "production"}}}
	if len(pass.asked) != 1 || !reflect.DeepEqual(pass.asked[0], want) {
		t.Errorf("the pass was asked %+v, want %+v: the load balancer belongs to every environment and app of the tier", pass.asked, want)
	}
}

func TestTheTiersLoadBalancerReadsNoProjectsBindings(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{
		albresources.SurfaceHTTPSProxy: {"sslPolicy": outputOf("custom", "tls", "policy")},
	}}}
	_, err := EvaluateFront(context.Background(), pass, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "httpsProxy.sslPolicy") {
		t.Errorf("EvaluateFront() = %v, want a refusal naming the field a binding output fills", err)
	}
}

func TestAPatchOnAResourceTheEdgeDoesNotConstructIsRefusedByName(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{"gate": {"description": "x"}}}}
	_, err := EvaluateFront(context.Background(), pass, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "gate") {
		t.Errorf("EvaluateFront() = %v, want the unknown surface refused by name", err)
	}
}

type namedBindings struct{ bindings []provider.Binding }

func (b namedBindings) Names(context.Context) ([]string, error) {
	names := make([]string, 0, len(b.bindings))
	for _, binding := range b.bindings {
		names = append(names, binding.Name)
	}
	return names, nil
}

func (b namedBindings) Published(context.Context) ([]provider.Binding, error) { return b.bindings, nil }

func (b namedBindings) Named(_ context.Context, name string) (provider.Binding, error) {
	for _, binding := range b.bindings {
		if binding.Name == name {
			return binding, nil
		}
	}
	return provider.Binding{}, errors.New("no binding " + name)
}

type kindOnly struct {
	edge.Edge
	kind edge.Kind
}

func (k kindOnly) Kind() edge.Kind { return k.kind }

func appStack(app string) provider.StackSpec {
	return provider.StackSpec{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.AppStack("prod", app, naming.NewReleaseToken("b1", ""))},
		Kind:     provider.StackApp,
		Edge:     kindOnly{kind: albresources.Kind},
		App:      &provider.AppSpec{App: app},
		Bindings: namedBindings{bindings: []provider.Binding{{Name: "armor", Properties: map[string]string{"policy": "web-armor"}}}},
	}
}

func TestAnAppsEdgePatchReadsTheBindingOutputsOfTheStackItShipsWith(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{
		albresources.SurfaceBackendService: {"securityPolicy": outputOf("custom", "armor", "policy")},
	}}}
	result, err := EvaluateApp(context.Background(), pass, appStack("web"))
	if err != nil {
		t.Fatalf("EvaluateApp() = %v", err)
	}
	if got := result.Patches[albresources.SurfaceBackendService]["securityPolicy"]; got != "web-armor" {
		t.Errorf("the backend service's security policy = %v, want the value the published record holds", got)
	}
	want := transform.Request{Provider: "gcp", EnvTier: "production", Resources: []transform.Resource{{Type: albresources.TransformKindEdge, Name: "web", App: "web"}}}
	if len(pass.asked) != 1 || !reflect.DeepEqual(pass.asked[0], want) {
		t.Errorf("the pass was asked %+v, want %+v: one backend serves a hostname in every environment of the tier", pass.asked, want)
	}
}

func TestAnAppsHeaderPatchThatIsNoListOfHeadersIsRefusedWhenItIsEvaluated(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{
		albresources.SurfaceBackendService: {"customResponseHeaders": []any{float64(1)}},
	}}}
	_, err := EvaluateApp(context.Background(), pass, appStack("web"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "edge web's backendService.customResponseHeaders") {
		t.Errorf("EvaluateApp() = %v, want the header patch refused by name before anything is raised", err)
	}
}

func TestAnAppBehindAnotherEdgeIsNotEvaluatedForTheLoadBalancer(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{}
	spec := appStack("web")
	spec.Edge = kindOnly{kind: edge.Kind("cloudflare")}
	if _, err := EvaluateApp(context.Background(), pass, spec); err != nil || len(pass.asked) != 0 {
		t.Errorf("EvaluateApp() = %v after asking %v, want nothing asked: the app serves through no load balancer of this edge", err, pass.asked)
	}
}

func TestAnAppsEdgePatchToAFieldOcelOwnsIsRefusedWhenItIsEvaluated(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{
		albresources.SurfaceBackendService: {"cdnPolicy": nil},
	}}}
	_, err := EvaluateApp(context.Background(), pass, appStack("web"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "edge web's backendService.cdnPolicy.bypassCacheOnRequestHeaders") {
		t.Errorf("EvaluateApp() = %v, want the owned field the patch replaces refused by name", err)
	}
}

func TestAFrontPatchToAFieldOcelOwnsIsRefusedWhenItIsEvaluated(t *testing.T) {
	t.Parallel()

	pass := &evaluatingPass{result: transform.Result{Patches: transform.Patches{
		albresources.SurfaceURLMap: {"defaultService": "elsewhere"},
	}}}
	_, err := EvaluateFront(context.Background(), pass, environment.TierProduction)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "the production tier's load balancer's urlMap.defaultService") {
		t.Errorf("EvaluateFront() = %v, want the owned field refused by name", err)
	}
}
