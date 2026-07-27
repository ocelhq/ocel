package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// setStoreWorkerBundle writes a deployments-store worker bundle and exports
// the manifest pointing Cloudflare at it, standing in for the npm launcher
// (mirrors edgeworker_test.go's setWorkerBundle for the generic worker).
func setStoreWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.StoreBundleManifest{edge.KindCloudflare: bundle})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.EnvStoreWorkerBundles, string(raw))
}

// TestRootStackSpecs_ThreadsEdgeValues guards ocelhq-f0e: the generic worker's
// OCEL_CACHE_STORE R2 binding degrades to its no-store fallback in every
// production deploy unless the bootstrap values carrying the cache bucket
// name reach RootStackSpec, exactly like they already reach AppDeployment.Values
// for a preview deploy (edgeworker.go's Values: cfg.EdgeValues).
func TestRootStackSpecs_ThreadsEdgeValues(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	cfg := Config{Edge: &recordingEdge{}, EdgeValues: map[string]string{"cacheBucket": "ocel-proj-cache"}}

	t.Run("no worker-fronted apps", func(t *testing.T) {
		manifest := &deploymentsv1.Manifest{Slug: "proj"}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
		if specs[0].Values["cacheBucket"] != "ocel-proj-cache" {
			t.Errorf("specs[0].Values = %v, want cacheBucket=ocel-proj-cache", specs[0].Values)
		}
	})

	t.Run("with a worker-fronted app", func(t *testing.T) {
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
		if specs[0].Values["cacheBucket"] != "ocel-proj-cache" {
			t.Errorf("specs[0].Values = %v, want cacheBucket=ocel-proj-cache", specs[0].Values)
		}
	})
}

// Production's worker routes are project-lifetime and reconciled declaratively,
// so a hostname dropped from the config must lose its route. PruneRoutes is a
// bool whose zero value is "prune nothing", so a spec that leaves it unset
// silently stops pruning (ocelhq-5w3).
func TestRootStackSpecs_ProductionPrunesStaleRoutes(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	cfg := Config{Edge: &recordingEdge{}, Class: deploymentsv1.Environment_CLASS_PRODUCTION}

	specs, err := rootStackSpecs(cfg, &deploymentsv1.Manifest{Slug: "proj"}, "v1", nil)
	if err != nil {
		t.Fatalf("rootStackSpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	if !specs[0].PruneRoutes {
		t.Error("PruneRoutes = false, want true for production")
	}
}

func TestPreviewBaseDomain(t *testing.T) {
	cases := map[string]string{
		"*.preview.acme.com": "preview.acme.com",
		"*.acme.com":         "acme.com",
		"acme.com":           "",
		"":                   "",
	}
	for in, want := range cases {
		if got := previewBaseDomain(in); got != want {
			t.Errorf("previewBaseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreviewWildcard(t *testing.T) {
	cases := map[string]string{
		"preview.acme.com": "*.preview.acme.com",
		"":                 "",
	}
	for in, want := range cases {
		if got := previewWildcard(in); got != want {
			t.Errorf("previewWildcard(%q) = %q, want %q", in, got, want)
		}
	}
}

// One route per pointer, so concurrent previews of one project stop overwriting a
// shared wildcard route (ocelhq-5w3). The base domain is carried alongside because
// the concrete hostname it resolves to has nothing to recover it from.
func TestPreviewHostnames_ResolvesTheWildcardToThePointerHost(t *testing.T) {
	declared := map[string][]string{"web": {"*.preview.acme.com"}}
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}

	got, err := previewHostnames(cfg, declared)
	if err != nil {
		t.Fatalf("previewHostnames: %v", err)
	}
	want := previewRouteHost("proj", "web", "pr-42", "preview.acme.com")
	if len(got.routes["web"]) != 1 || got.routes["web"][0] != want {
		t.Fatalf("route hostnames = %v, want [%s]", got.routes["web"], want)
	}
	if strings.Contains(got.routes["web"][0], "*") {
		t.Errorf("preview route hostname %q is still a wildcard", got.routes["web"][0])
	}
	if got.baseDomains["web"] != "preview.acme.com" {
		t.Errorf("base domain = %q, want preview.acme.com", got.baseDomains["web"])
	}
}

func TestResolveWorkerHostnames_ProductionServesItsDeclaredHostnames(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains:   map[string]*deploymentsv1.DomainList{"production": {Hostnames: []string{"acme.com", "www.acme.com"}}},
	}
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION, Slug: "proj"}

	resolved, err := resolveWorkerHostnames(cfg, manifest, workerApps(manifest))
	if err != nil {
		t.Fatalf("resolveWorkerHostnames: %v", err)
	}
	if want := []string{"acme.com", "www.acme.com"}; !slicesEqual(resolved.routes["web"], want) {
		t.Errorf("route hostnames = %v, want the declared hostnames %v", resolved.routes["web"], want)
	}
}

// An app declaring no preview domain is served on the edge's own vendor
// subdomain: no route, and no error — the legitimate no-route deploy.
func TestResolveWorkerHostnames_PreviewWithNoDeclaredDomainGetsNoRoute(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}

	resolved, err := resolveWorkerHostnames(cfg, manifest, workerApps(manifest))
	if err != nil {
		t.Fatalf("resolveWorkerHostnames: %v", err)
	}
	if len(resolved.routes["web"]) != 0 {
		t.Errorf("route hostnames = %v, want none", resolved.routes["web"])
	}
}

// A preview declaration that is not a wildcard has no base to hang a pointer host
// off, so the deploy would attach no route at all and every request 404 with
// nothing having failed. It must fail instead, naming the app and the domain.
func TestRootStackSpecs_PreviewWithoutAWildcardFailsTheDeploy(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains:   map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"app.acme.com"}}},
	}
	cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42"}

	_, err := rootStackSpecs(cfg, manifest, "v1", nil)
	if err == nil {
		t.Fatal("expected a preview whose declared domain is not a wildcard to fail the deploy")
	}
	if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "app.acme.com") {
		t.Errorf("error must name the app and the offending domain, got %q", err)
	}
}

// The pointer cap is mirrored in cli/internal/previewid and scripts/e2e-next
// (separate modules, no shared constant), so drift is only ever caught here: an
// over-long label yields a route the edge rejects or a hostname that never
// resolves, with no deploy-time diagnostic.
func TestPreviewHostnames_OverLongPointerFailsTheDeploy(t *testing.T) {
	pointer := strings.Repeat("p", previewPointerMaxLen+1)
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: pointer}

	_, err := previewHostnames(cfg, map[string][]string{"web": {"*.preview.acme.com"}})
	if err == nil {
		t.Fatalf("expected a pointer of %d characters to fail the deploy", len(pointer))
	}
	if !strings.Contains(err.Error(), pointer) || !strings.Contains(err.Error(), "64") {
		t.Errorf("error must name the pointer and the label length, got %q", err)
	}
}

func TestWorkerAppURL(t *testing.T) {
	cases := []struct {
		name    string
		domains []string
		want    string
	}{
		{"custom domain", []string{"app.acme.com"}, "https://app.acme.com"},
		{"features first non-wildcard", []string{"*.acme.com", "app.acme.com"}, "https://app.acme.com"},
		{"all-wildcard falls back to first", []string{"*.acme.com"}, "https://*.acme.com"},
		{"no domain", nil, ""},
		{"a preview's resolved pointer host is reported as-is", []string{"pr-42-abc.preview.acme.com"}, "https://pr-42-abc.preview.acme.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerAppURL(tc.domains); got != tc.want {
				t.Errorf("workerAppURL(%v) = %q, want %q", tc.domains, got, tc.want)
			}
		})
	}
}

func TestPreviewGenericName_DistinctFromProduction(t *testing.T) {
	prod := workerScriptName("proj-production", "web")
	preview := previewGenericName("proj", "web")
	if prod == preview {
		t.Fatalf("preview root worker name %q collides with production", preview)
	}
	if want := "ocel-proj-preview-web"; preview != want {
		t.Errorf("previewGenericName = %q, want %q", preview, want)
	}
}

func TestPreviewWorkerPrefix_PrefixesEveryPreviewWorkerAndNotProduction(t *testing.T) {
	prefix := previewWorkerPrefix("shop")
	if want := "ocel-shop-preview"; prefix != want {
		t.Fatalf("previewWorkerPrefix = %q, want %q", prefix, want)
	}
	// Every worker a preview deploys — the per-app generic workers and the
	// no-app "root" fallback — must sit under the prefix teardown sweeps, so a
	// teardown finds them without the store history that named them.
	for _, app := range []string{"web", "api", "root"} {
		name := previewGenericName("shop", app)
		if !strings.HasPrefix(name, prefix+"-") {
			t.Errorf("previewGenericName(%q) = %q, not under prefix %q-", app, name, prefix)
		}
	}
	// The prefix must never reach a production worker of the same project, or a
	// destroy --preview would take production down.
	if prod := workerScriptName("shop-production", "web"); strings.HasPrefix(prod, previewWorkerPrefix("shop")+"-") {
		t.Errorf("production worker %q matches the preview prefix %q-", prod, previewWorkerPrefix("shop"))
	}
}

// A preview reconciles one exact route per pointer, prunes nothing (its sibling
// pointers' routes hang off the same shared script), and requires — never plants
// — the wildcard record every pointer hostname resolves through (ocelhq-5w3).
func TestRootStackSpecs_PreviewRoutesOnePointerExactHostname(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains:   map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.acme.com"}}},
	}
	cfg := Config{
		Edge:     &recordingEdge{},
		Slug:     "proj",
		Class:    deploymentsv1.Environment_CLASS_PREVIEW,
		Identity: "pr-42",
	}

	specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
	if err != nil {
		t.Fatalf("rootStackSpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	spec := specs[0]
	if spec.GenericName != "ocel-proj-preview-web" {
		t.Errorf("GenericName = %q, want ocel-proj-preview-web", spec.GenericName)
	}
	wantHost := previewRouteHost("proj", "web", "pr-42", "preview.acme.com")
	if len(spec.Domains) != 1 || spec.Domains[0] != wantHost {
		t.Errorf("Domains = %v, want the pointer-exact host [%s]", spec.Domains, wantHost)
	}
	if spec.PruneRoutes {
		t.Error("PruneRoutes = true: pruning would delete a sibling pointer's route off the shared script")
	}
	if spec.RequiredRecord != "*.preview.acme.com" {
		t.Errorf("RequiredRecord = %q, want the declared wildcard *.preview.acme.com", spec.RequiredRecord)
	}
	if spec.Generic.Vars[envPreview] != "1" {
		t.Errorf("Vars[%s] = %q, want 1", envPreview, spec.Generic.Vars[envPreview])
	}
	// Emitted empty, the worker leaves preview mode and every preview request
	// 404s with nothing failing at deploy time — so it must survive the switch to
	// a concrete Domains, which carries no wildcard to recover it from.
	if spec.Generic.Vars[envPreviewBaseDomain] != "preview.acme.com" {
		t.Errorf("Vars[%s] = %q, want preview.acme.com", envPreviewBaseDomain, spec.Generic.Vars[envPreviewBaseDomain])
	}
	// The worker recovers the pointer by stripping this exact suffix off the
	// request's subdomain label; empty or wrong, it reads the whole label as the
	// pointer and resolves nothing.
	suffix := spec.Generic.Vars[envPreviewLabelSuffix]
	if suffix != previewRouteSuffix("proj", "web") {
		t.Errorf("Vars[%s] = %q, want %q", envPreviewLabelSuffix, suffix, previewRouteSuffix("proj", "web"))
	}
	label, _, _ := strings.Cut(spec.Domains[0], ".")
	if got := strings.TrimSuffix(label, suffix); got != "pr-42" {
		t.Errorf("stripping Vars[%s] off label %q gave %q, want the pointer pr-42", envPreviewLabelSuffix, label, got)
	}
	if spec.Generic.Vars["OCEL_APP"] != "web" {
		t.Errorf("Vars[OCEL_APP] = %q, want web", spec.Generic.Vars["OCEL_APP"])
	}
}

// Production's routes are project-lifetime and reconciled declaratively: it keeps
// pruning, plants its own records, and serves its declared hostnames verbatim.
func TestRootStackSpecs_ProductionKeepsDeclarativeHostnames(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains:   map[string]*deploymentsv1.DomainList{"production": {Hostnames: []string{"acme.com", "www.acme.com"}}},
	}
	cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PRODUCTION}

	specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
	if err != nil {
		t.Fatalf("rootStackSpecs: %v", err)
	}
	spec := specs[0]
	if !slicesEqual(spec.Domains, []string{"acme.com", "www.acme.com"}) {
		t.Errorf("Domains = %v, want the declared hostnames", spec.Domains)
	}
	if !spec.PruneRoutes {
		t.Error("PruneRoutes = false, want true for production")
	}
	if spec.RequiredRecord != "" {
		t.Errorf("RequiredRecord = %q, want empty: production plants its own records", spec.RequiredRecord)
	}
}

// A preview's teardown deletes exactly the routes its deploy registered, which it
// reads back off the record — so the record has to carry them. Production carries
// none: its routes outlive any one deploy.
func TestBuildDeploymentRecord_RouteHostnamesByClass(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains: map[string]*deploymentsv1.DomainList{
			"preview":    {Hostnames: []string{"*.preview.acme.com"}},
			"production": {Hostnames: []string{"acme.com"}},
		},
	}
	app := manifest.GetApps()[0]
	root := writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`})

	t.Run("preview records its pointer-exact host", func(t *testing.T) {
		cfg := Config{
			ArtifactRoot: root,
			Slug:         "proj",
			Class:        deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:     "pr-42",
		}
		record, err := buildDeploymentRecord(cfg, manifest, app, "WEB1", nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		want := previewRouteHost("proj", "web", "pr-42", "preview.acme.com")
		if !slicesEqual(record.RouteHostnames, []string{want}) {
			t.Errorf("RouteHostnames = %v, want [%s]", record.RouteHostnames, want)
		}
	})

	t.Run("production records none", func(t *testing.T) {
		cfg := Config{
			ArtifactRoot: root,
			Slug:         "proj",
			Class:        deploymentsv1.Environment_CLASS_PRODUCTION,
		}
		record, err := buildDeploymentRecord(cfg, manifest, app, "WEB1", nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if len(record.RouteHostnames) != 0 {
			t.Errorf("RouteHostnames = %v, want none: production routes are project-lifetime", record.RouteHostnames)
		}
	})
}

// The generic worker is AWS_IAM-gated behind its Lambdas, so it must be handed
// the edge reader's key to sign forwards — the access key as a plain var, the
// secret key as a secret binding (never plaintext).
func TestRootStackSpecs_BindsEdgeSigningCredentials(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}

	t.Run("bound when the substrate has edge credentials", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret-edge"}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		g := specs[0].Generic
		if g.Vars[edge.EdgeAccessKeyIDVar] != "AKIAEDGE" {
			t.Errorf("generic Vars[%s] = %q, want AKIAEDGE", edge.EdgeAccessKeyIDVar, g.Vars[edge.EdgeAccessKeyIDVar])
		}
		if g.Secrets[edge.EdgeSecretKeyVar] != "secret-edge" {
			t.Errorf("generic Secrets[%s] = %q, want secret-edge", edge.EdgeSecretKeyVar, g.Secrets[edge.EdgeSecretKeyVar])
		}
		if _, leaked := g.Vars[edge.EdgeSecretKeyVar]; leaked {
			t.Error("the signing secret must never appear in plain-text Vars")
		}
	})

	t.Run("absent on a substrate predating edge credentials", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		g := specs[0].Generic
		if _, ok := g.Vars[edge.EdgeAccessKeyIDVar]; ok {
			t.Error("no access-key var expected without edge credentials")
		}
		if g.Secrets[edge.EdgeSecretKeyVar] != "" {
			t.Error("no secret expected without edge credentials")
		}
	})
}

// The worker's cache entrypoint addresses S3 and DynamoDB itself, so it must be
// handed their coordinates. They are account-global, so they ride as vars on the
// frozen bundle rather than in each Deployment record — and the region is bound
// rather than parsed off a Function URL host, which an all-edge app has none of.
func TestRootStackSpecs_BindsCacheCoordinates(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}

	t.Run("bound from the substrate's stores", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1", StateTable: "state-abc", AssetBucket: "assets-xyz"}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		vars := specs[0].Generic.Vars
		for name, want := range map[string]string{
			edge.AWSRegionVar:   "eu-west-1",
			edge.StateTableVar:  "state-abc",
			edge.AssetBucketVar: "assets-xyz",
		} {
			if vars[name] != want {
				t.Errorf("generic Vars[%s] = %q, want %q", name, vars[name], want)
			}
		}
	})

	t.Run("absent on a substrate predating a store", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1"}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		vars := specs[0].Generic.Vars
		for _, name := range []string{edge.StateTableVar, edge.AssetBucketVar} {
			if _, ok := vars[name]; ok {
				t.Errorf("Vars[%s] must be unset, not bound empty", name)
			}
		}
	})
}

func TestFinalizeProductionDeploy_ReconcileThenStageThenPromoteInOrder(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1", GenericName: "web-generic"}}
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
		{App: "api", BuildID: "b2", Record: edge.DeploymentRecord{App: "api", BuildID: "b2"}},
	}

	state, err := finalizeDeploy(ctx, fake, specs, nil, "promo1", "", "", 100, results)
	if err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	if len(fake.reconciles) != 1 {
		t.Fatalf("reconciles = %d, want 1", len(fake.reconciles))
	}
	if len(fake.staged) != 2 {
		t.Fatalf("staged = %d, want 2 (one per app)", len(fake.staged))
	}
	if len(fake.promotions) != 1 {
		t.Fatalf("promotions = %d, want 1", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" {
		t.Errorf("promotion id = %q, want %q", fake.promotions[0].PromotionID, "promo1")
	}
	want := map[string]string{"web": "b1", "api": "b2"}
	if len(fake.promotions[0].Builds) != len(want) {
		t.Fatalf("promotion builds = %v, want %v", fake.promotions[0].Builds, want)
	}
	for app, buildID := range want {
		if got := fake.promotions[0].Builds[app]; got != buildID {
			t.Errorf("promotion.Builds[%q] = %q, want %q", app, got, buildID)
		}
	}
	if state[edge.RootStackKeyEndpoint] == "" {
		t.Error("expected a reconciled state to be returned")
	}
}

func TestFinalizeProductionDeploy_StampsTheTagOntoThePromotion(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	if _, err := finalizeDeploy(ctx, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "v1.2.3", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	if len(fake.promotions) != 1 || fake.promotions[0].Tag != "v1.2.3" {
		t.Errorf("promotions = %v, want the promote to carry tag %q", fake.promotions, "v1.2.3")
	}
}

func TestFinalizeDeploy_PromotesTheGivenPointer(t *testing.T) {
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	prod := &recordingRootStack{}
	if _, err := finalizeDeploy(ctx, prod, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy(production): %v", err)
	}
	if len(prod.promotePointers) != 1 || prod.promotePointers[0] != "" {
		t.Errorf("production promote pointer = %v, want the reserved default (empty)", prod.promotePointers)
	}

	preview := &recordingRootStack{}
	if _, err := finalizeDeploy(ctx, preview, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "pr-42", 100, results); err != nil {
		t.Fatalf("finalizeDeploy(preview): %v", err)
	}
	if len(preview.promotePointers) != 1 || preview.promotePointers[0] != "pr-42" {
		t.Errorf("preview promote pointer = %v, want [pr-42]", preview.promotePointers)
	}
}

func TestPromotePointer_EmptyForProductionIdentityForPreview(t *testing.T) {
	if got := promotePointer(Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION, Identity: "ignored"}); got != "" {
		t.Errorf("promotePointer(production) = %q, want empty", got)
	}
	if got := promotePointer(Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42"}); got != "pr-42" {
		t.Errorf("promotePointer(preview) = %q, want pr-42", got)
	}
}

func TestPlanEnvironment_ThreadsClassLifecycleIdentity(t *testing.T) {
	cfg := Config{
		Class:     deploymentsv1.Environment_CLASS_PREVIEW,
		Lifecycle: deploymentsv1.Environment_LIFECYCLE_EPHEMERAL,
		Identity:  "pr-42",
	}
	env := planEnvironment(cfg)
	if env.GetClass() != deploymentsv1.Environment_CLASS_PREVIEW {
		t.Errorf("class = %s, want preview", env.GetClass())
	}
	if env.GetLifecycle() != deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		t.Errorf("lifecycle = %s, want ephemeral", env.GetLifecycle())
	}
	if env.GetIdentity() != "pr-42" {
		t.Errorf("identity = %q, want pr-42", env.GetIdentity())
	}
}

func TestBootstrapCommand_ByClass(t *testing.T) {
	if got := bootstrapCommand(Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION}); got != "ocel bootstrap" {
		t.Errorf("bootstrapCommand(production) = %q", got)
	}
	if got := bootstrapCommand(Config{Class: deploymentsv1.Environment_CLASS_PREVIEW}); got != "ocel bootstrap --preview" {
		t.Errorf("bootstrapCommand(preview) = %q", got)
	}
}

func TestValidateTag_RejectsMalformedTagsHostSide(t *testing.T) {
	if err := validateTag(""); err != nil {
		t.Errorf("validateTag(\"\") = %v, want nil (untagged default)", err)
	}
	if err := validateTag("v1.2.3"); err != nil {
		t.Errorf("validateTag(%q) = %v, want nil", "v1.2.3", err)
	}
	for _, bad := range []string{"feature/x", "has space", "über"} {
		if err := validateTag(bad); err == nil {
			t.Errorf("validateTag(%q) = nil, want an error", bad)
		}
	}
}

func TestCheckTagAvailable_RejectsATagAlreadyInUse(t *testing.T) {
	fake := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if err := checkTagAvailable(ctx, fake, state, "v1.2.3"); err == nil {
		t.Fatal("expected a duplicate tag to be rejected")
	}
}

func TestCheckTagAvailable_AllowsAFreshTag(t *testing.T) {
	fake := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
		},
	}
	ctx := context.Background()
	state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	if err := checkTagAvailable(ctx, fake, state, "v2.0.0"); err != nil {
		t.Errorf("checkTagAvailable rejected a fresh tag: %v", err)
	}
}

func TestCheckTagAvailable_NoOpForUntaggedOrFirstDeploy(t *testing.T) {
	fake := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
		},
	}
	ctx := context.Background()

	// Untagged deploy: no check regardless of history.
	if err := checkTagAvailable(ctx, fake, edge.RootStackState{edge.RootStackKeyEndpoint: "http://store"}, ""); err != nil {
		t.Errorf("untagged deploy should never fail the tag check: %v", err)
	}
	// First-ever deploy: no store yet (no endpoint), so no history to read.
	if err := checkTagAvailable(ctx, fake, nil, "v1.2.3"); err != nil {
		t.Errorf("first deploy (no store) should never fail the tag check: %v", err)
	}
}

func TestFinalizeProductionDeploy_StagesBeforeAnyPromote(t *testing.T) {
	fake := &orderTrackingRootStack{recordingRootStack: &recordingRootStack{}}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
	}

	if _, err := finalizeDeploy(ctx, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	want := []string{"reconcile", "stage", "promote"}
	if len(fake.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	for i, c := range want {
		if fake.calls[i] != c {
			t.Errorf("calls[%d] = %q, want %q (full sequence: %v)", i, fake.calls[i], c, fake.calls)
		}
	}
}

func TestFinalizeProductionDeploy_AppFailureAbortsPromote(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}},
		{App: "api", Err: errors.New("app-deploy stack failed")},
	}

	_, err := finalizeDeploy(ctx, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results)
	if err == nil {
		t.Fatal("expected an error when one app's deploy failed")
	}

	if len(fake.staged) != 1 {
		t.Errorf("staged = %d, want 1 (only the successful app)", len(fake.staged))
	}
	if len(fake.promotions) != 0 {
		t.Errorf("promotions = %d, want 0: a failed app must abort the promote", len(fake.promotions))
	}
}

func TestFinalizeProductionDeploy_SecondDeployProducesNewPromotionRetainingPrior(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1"}}
	results := []appDeployResult{{App: "web", BuildID: "b1", Record: edge.DeploymentRecord{App: "web", BuildID: "b1"}}}

	state, err := finalizeDeploy(ctx, fake, specs, nil, "promo1", "", "", 100, results)
	if err != nil {
		t.Fatalf("first finalizeDeploy: %v", err)
	}

	results2 := []appDeployResult{{App: "web", BuildID: "b2", Record: edge.DeploymentRecord{App: "web", BuildID: "b2"}}}
	if _, err := finalizeDeploy(ctx, fake, specs, state, "promo2", "", "", 200, results2); err != nil {
		t.Fatalf("second finalizeDeploy: %v", err)
	}

	if len(fake.promotions) != 2 {
		t.Fatalf("promotions = %d, want 2 (both retained)", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" || fake.promotions[1].PromotionID != "promo2" {
		t.Errorf("promotions = %+v, want promo1 then promo2", fake.promotions)
	}
}

// orderTrackingRootStack wraps recordingRootStack to additionally record the
// relative order of reconcile/stage/promote calls, which recordingRootStack's
// own per-kind slices cannot express on their own.
type orderTrackingRootStack struct {
	*recordingRootStack
	calls []string
}

func (f *orderTrackingRootStack) ReconcileRootStack(ctx context.Context, spec edge.RootStackSpec, prior edge.RootStackState) (edge.RootStackState, error) {
	f.calls = append(f.calls, "reconcile")
	return f.recordingRootStack.ReconcileRootStack(ctx, spec, prior)
}

func (f *orderTrackingRootStack) PutStaged(ctx context.Context, state edge.RootStackState, record edge.DeploymentRecord) error {
	f.calls = append(f.calls, "stage")
	return f.recordingRootStack.PutStaged(ctx, state, record)
}

func (f *orderTrackingRootStack) Promote(ctx context.Context, state edge.RootStackState, promotion edge.Promotion, pointer string) error {
	f.calls = append(f.calls, "promote")
	return f.recordingRootStack.Promote(ctx, state, promotion, pointer)
}

func TestReconcileRootStack_ThreadsStateAcrossMultipleSpecs(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{
		{Version: "v1", GenericName: "web-generic"},
		{Version: "v1", GenericName: "admin-generic"},
	}

	state, err := reconcileRootStack(ctx, fake, specs, nil)
	if err != nil {
		t.Fatalf("reconcileRootStack: %v", err)
	}
	if len(fake.reconciles) != 2 {
		t.Fatalf("reconciles = %d, want 2 (one per spec)", len(fake.reconciles))
	}
	if state[edge.RootStackKeyEndpoint] == "" {
		t.Error("expected a non-empty reconciled state")
	}
}

func TestReconcileRootStack_NoSpecsReturnsPriorUnchanged(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	prior := edge.RootStackState{edge.RootStackKeyEndpoint: "https://prior"}

	state, err := reconcileRootStack(ctx, fake, nil, prior)
	if err != nil {
		t.Fatalf("reconcileRootStack: %v", err)
	}
	if len(fake.reconciles) != 0 {
		t.Errorf("reconciles = %d, want 0", len(fake.reconciles))
	}
	if state[edge.RootStackKeyEndpoint] != "https://prior" {
		t.Errorf("state = %v, want prior unchanged", state)
	}
}
