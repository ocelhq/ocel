package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
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
	// Production apps hold hostnames of their own on workers of their own, so a
	// stem spanning them would let one app's reconcile sweep another's route —
	// and one spanning the project would sweep its previews.
	if specs[0].PruneWorkerStem != "" {
		t.Errorf("PruneWorkerStem = %q, want empty: a production spec sweeps its own script alone", specs[0].PruneWorkerStem)
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

// The project owns the wildcard, so every app resolves under one base domain and
// each app gets its own label below it. The base is carried alongside because the
// concrete hostnames have nothing to recover it from.
func TestPreviewHostnames_ResolvesTheWildcardToThePointerHosts(t *testing.T) {
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}

	t.Run("a single app elides the app label", func(t *testing.T) {
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}}
		got, err := previewHostnames(cfg, apps, map[string][]string{"web": {"*.preview.acme.com"}})
		if err != nil {
			t.Fatalf("previewHostnames: %v", err)
		}
		if want := []string{"pr-42.preview.acme.com"}; !slicesEqual(got.hosts["web"], want) {
			t.Errorf("hosts = %v, want %v", got.hosts["web"], want)
		}
		if got.previewBase != "preview.acme.com" {
			t.Errorf("previewBase = %q, want preview.acme.com", got.previewBase)
		}
	})

	t.Run("two apps qualify the label", func(t *testing.T) {
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}
		declared := map[string][]string{"web": {"*.preview.acme.com"}, "api": {"*.preview.acme.com"}}
		got, err := previewHostnames(cfg, apps, declared)
		if err != nil {
			t.Fatalf("previewHostnames: %v", err)
		}
		if want := []string{"pr-42--web.preview.acme.com"}; !slicesEqual(got.hosts["web"], want) {
			t.Errorf("web hosts = %v, want %v", got.hosts["web"], want)
		}
		if want := []string{"pr-42--api.preview.acme.com"}; !slicesEqual(got.hosts["api"], want) {
			t.Errorf("api hosts = %v, want %v", got.hosts["api"], want)
		}
	})
}

// The wildcard is claimed by the project, so an app that declares nothing of its
// own is still served under it — there is no per-app preview domain to inherit.
func TestPreviewHostnames_ServesAnAppThatDeclaresNothingUnderTheProjectWildcard(t *testing.T) {
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
	apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}

	got, err := previewHostnames(cfg, apps, map[string][]string{"web": {"*.preview.acme.com"}})
	if err != nil {
		t.Fatalf("previewHostnames: %v", err)
	}
	if want := []string{"pr-42--api.preview.acme.com"}; !slicesEqual(got.hosts["api"], want) {
		t.Errorf("api hosts = %v, want %v", got.hosts["api"], want)
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
	if want := []string{"acme.com", "www.acme.com"}; !slicesEqual(resolved.hosts["web"], want) {
		t.Errorf("hostnames = %v, want the declared hostnames %v", resolved.hosts["web"], want)
	}
}

// A preview with no declared domain used to be served on the edge's own vendor
// subdomain, which silently answers off the production default pointer. There is
// no preview without a project-owned domain, so the deploy is refused instead.
func TestRootStackSpecs_PreviewWithNoDeclaredDomainIsRefused(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}
	cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42"}

	_, err := rootStackSpecs(cfg, manifest, "v1", nil)
	if err == nil {
		t.Fatal("expected a preview deploy with no declared preview domain to be refused")
	}
	if !strings.Contains(err.Error(), "domains.preview") {
		t.Errorf("error must point at the config field to add, got %q", err)
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

// One project, one preview wildcard: two apps claiming different bases would need
// two entrypoint workers, which is the model this replaced.
func TestPreviewHostnames_TwoPreviewDomainsInOneProjectAreRefused(t *testing.T) {
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
	apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}
	declared := map[string][]string{"web": {"*.preview.acme.com"}, "api": {"*.preview.other.com"}}

	_, err := previewHostnames(cfg, apps, declared)
	if err == nil {
		t.Fatal("expected two project preview domains to be refused")
	}
	if !strings.Contains(err.Error(), "*.preview.acme.com") || !strings.Contains(err.Error(), "*.preview.other.com") {
		t.Errorf("error must name both domains, got %q", err)
	}
}

// The pointer cap is mirrored in cli/internal/previewid and scripts/e2e-next
// (separate modules, no shared constant), so drift is only ever caught here: an
// over-long label yields a hostname that never resolves, with no deploy-time
// diagnostic.
func TestPreviewHostnames_OverLongPointerFailsTheDeploy(t *testing.T) {
	pointer := strings.Repeat("p", previewLabelMaxLen+1)
	cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: pointer}
	apps := []*deploymentsv1.ManifestApp{{Name: "web"}}

	_, err := previewHostnames(cfg, apps, map[string][]string{"web": {"*.preview.acme.com"}})
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

func TestPreviewWorkerName_ProjectScopedAndDistinctFromProduction(t *testing.T) {
	name := previewWorkerName("shop")
	if want := "ocel-shop--preview"; name != want {
		t.Fatalf("previewWorkerName = %q, want %q", name, want)
	}
	// The worker resolves the app from the request host, so no app ever appears in
	// its name — and it must never reach this project's production workers.
	if prod := workerScriptName("shop", "prod", "web"); prod == name || strings.HasPrefix(prod, name) {
		t.Errorf("production worker %q collides with the preview worker %q", prod, name)
	}
}

// One entrypoint worker for the whole project, attached to the wildcard the
// project declared, pruning everything else off itself and planting the record
// behind it — the pointer is not in any of it.
func TestRootStackSpecs_PreviewIsOneProjectScopedSpec(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}, {Name: "api", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"},
			{LogicalName: "api_index", Framework: "next", App: "api", RouteId: "/"},
		},
		Domains: map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.acme.com"}}},
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
		t.Fatalf("specs = %d, want 1 for the whole project", len(specs))
	}
	spec := specs[0]
	if spec.GenericName != "ocel-proj--preview" {
		t.Errorf("GenericName = %q, want ocel-proj--preview", spec.GenericName)
	}
	if !slicesEqual(spec.Domains, []string{"*.preview.acme.com"}) {
		t.Errorf("Domains = %v, want the declared wildcard", spec.Domains)
	}
	if !spec.PruneRoutes {
		t.Error("PruneRoutes = false: the wildcard is the project's complete desired route set")
	}
	if spec.RequiredRecord != "" {
		t.Errorf("RequiredRecord = %q, want empty: the project owns the base domain, so Ocel plants the record", spec.RequiredRecord)
	}
	// A pointer-exact route left on a per-app preview worker outranks the
	// wildcard, so the sweep has to reach that whole worker family — and no
	// further than it.
	if spec.PruneWorkerStem != previewWorkerName("proj") {
		t.Errorf("PruneWorkerStem = %q, want %q", spec.PruneWorkerStem, previewWorkerName("proj"))
	}
	if spec.Generic.Vars[envPreview] != "1" {
		t.Errorf("Vars[%s] = %q, want 1", envPreview, spec.Generic.Vars[envPreview])
	}
	// Emitted empty, the worker leaves preview mode and every preview request
	// 404s with nothing failing at deploy time.
	if spec.Generic.Vars[envPreviewBaseDomain] != "preview.acme.com" {
		t.Errorf("Vars[%s] = %q, want preview.acme.com", envPreviewBaseDomain, spec.Generic.Vars[envPreviewBaseDomain])
	}
	// The worker serves every app of the project and reads the app off the request
	// host, so baking one app into it would answer every host with that app. The
	// app list is what it recovers the app half of the label against instead.
	if app, ok := spec.Generic.Vars["OCEL_APP"]; ok {
		t.Errorf("Vars[OCEL_APP] = %q, want it unset for preview", app)
	}
	if got := spec.Generic.Vars[envPreviewApps]; got != "web,api" {
		t.Errorf("Vars[%s] = %q, want web,api", envPreviewApps, got)
	}
}

// A project with no worker-backed app still binds the list — empty, which the
// worker reads as "no apps" and 404s on, exactly as an unbound var would, so the
// two can never be told apart by accident.
func TestRootStackSpecs_PreviewAlwaysBindsTheAppList(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{Slug: "proj"}
	cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42"}

	specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
	if err != nil {
		t.Fatalf("rootStackSpecs: %v", err)
	}
	if _, ok := specs[0].Generic.Vars[envPreviewApps]; !ok {
		t.Errorf("Vars = %v, want %s bound even with no app to name", specs[0].Generic.Vars, envPreviewApps)
	}
}

func TestPreviewAppNames_IsALowercasedCommaSeparatedList(t *testing.T) {
	got := previewAppNames([]*deploymentsv1.ManifestApp{{Name: "Web"}, {Name: " admin "}, {Name: ""}})
	if got != "web,admin" {
		t.Errorf("previewAppNames = %q, want web,admin", got)
	}
	if got := previewAppNames(nil); got != "" {
		t.Errorf("previewAppNames(nil) = %q, want empty", got)
	}
}

// The claim check turns on this: a hostname held by one of the project's own
// workers is the project's to keep, and anything else is a conflict.
func TestProjectOwnsWorker(t *testing.T) {
	cases := []struct {
		name   string
		slug   string
		script string
		want   bool
	}{
		{"its own preview worker", "shop", previewWorkerName("shop"), true},
		{"its own production worker", "shop", workerScriptName("shop", "prod", "web"), true},
		{"another project's preview worker", "shop", previewWorkerName("other"), false},
		{"a hand-made worker", "shop", "my-worker", false},
		{"a sibling whose slug merely starts with ours", "shop", previewWorkerName("shopfoo"), false},
		// The reason worker names carry a doubled hyphen at the project
		// boundary: "shop-preview" is a valid slug, and reading its workers as
		// "shop"'s would let one project repoint and prune the other's routes.
		{"a sibling whose slug extends ours by a segment", "shop", previewWorkerName("shop-preview"), false},
		{"that sibling's production worker", "shop", workerScriptName("shop-preview", "prod", "web"), false},
		{"and ours is not theirs either", "shop-preview", previewWorkerName("shop"), false},
		{"no slug recognises nothing", "", previewWorkerName("shop"), false},
		{"no script", "shop", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProjectOwnsWorker(tc.slug, tc.script); got != tc.want {
				t.Errorf("ProjectOwnsWorker(%q, %q) = %v, want %v", tc.slug, tc.script, got, tc.want)
			}
		})
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

// A preview pointer owns no route of its own — the project's wildcard serves them
// all and is attached once for its lifetime — so a record has no hostname to
// carry and a teardown has nothing to read back off it.
func TestBuildDeploymentRecord_CarriesNoRouteHostnames(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		Domains:   map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.acme.com"}}},
	}
	cfg := Config{
		ArtifactRoot: writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`}),
		Slug:         "proj",
		Class:        deploymentsv1.Environment_CLASS_PREVIEW,
		Identity:     "pr-42",
	}

	record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "routeHostnames") {
		t.Errorf("record still carries route hostnames: %s", encoded)
	}
}

// One entrypoint worker fronts a project whose apps need not share a framework,
// so the Deployment says what can serve it. The worker reads an absent field as
// unsupported and 501s, so it must always be on the wire.
func TestBuildDeploymentRecord_CarriesTheFramework(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}, {Name: "docs"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}
	cfg := Config{
		ArtifactRoot: writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`}),
		Slug:         "proj",
	}

	record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.Framework != "next" {
		t.Errorf("Framework = %q, want next", record.Framework)
	}

	// An app declaring no framework still writes the field, empty: the worker
	// answers 501 for it rather than reading a record with no framework at all.
	bare, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[1], buildOnly("DOCS1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	encoded, err := json.Marshal(bare)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"framework":""`) {
		t.Errorf("record omits the framework field: %s", encoded)
	}
}

// varsManifest is one Next app carrying the variables a Deployment record has
// to be able to account for.
func varsManifest(variables ...*deploymentsv1.ManifestVariable) *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next", Variables: variables}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}
}

// varsConfig is the deploy configuration those variables are recorded under,
// which is nothing but the environment class the audit rule turns on.
func varsConfig(t *testing.T, class deploymentsv1.Environment_Class) Config {
	t.Helper()
	return Config{
		ArtifactRoot: writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`}),
		Slug:         "proj",
		Class:        class,
	}
}

// recordVariables is the audit half of a production record for one variable
// set, which is what every fingerprint claim below is read from.
func recordVariables(t *testing.T, variables ...*deploymentsv1.ManifestVariable) edge.DeploymentRecord {
	t.Helper()
	manifest := varsManifest(variables...)
	record, err := buildDeploymentRecord(varsConfig(t, deploymentsv1.Environment_CLASS_PRODUCTION), manifest, manifest.GetApps()[0], buildOnly("WEB1"), nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	return record
}

// An operator auditing a promotion needs the record to say which values it
// shipped: the fingerprint that distinguishes it from another Deployment of
// the same build, and the store coordinate and version of every key behind it.
func TestBuildDeploymentRecord_ProductionCarriesTheFingerprintAndPerKeyVersions(t *testing.T) {
	record := recordVariables(t,
		&deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2},
		&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Folder: "/admin", Version: 5},
	)

	if record.ValueFingerprint == "" {
		t.Errorf("record = %+v, want a fingerprint of what it shipped", record)
	}
	want := []edge.VariableRecord{
		{Key: "API_KEY", Folder: "/admin", Version: 5},
		{Key: "POSTHOG_ID", Version: 2},
	}
	if !reflect.DeepEqual(record.Variables, want) {
		t.Errorf("Variables = %+v, want %+v sorted by key", record.Variables, want)
	}
}

// The record's fingerprint is a digest of everything it recorded, not the
// identity's — that one covers baked values alone, so an app whose every
// variable is live would otherwise fingerprint as empty and two of its
// promotions would be indistinguishable in the ledger.
func TestBuildDeploymentRecord_AnAllLiveAppStillFingerprintsWhatItShipped(t *testing.T) {
	record := recordVariables(t,
		&deploymentsv1.ManifestVariable{Key: "SESSION_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Version: 7},
	)

	if record.ValueFingerprint == "" {
		t.Errorf("record = %+v, want a fingerprint even though nothing was baked", record)
	}
}

// Rotating one key is exactly what an audit has to be able to see, so two
// records over the same keys at different versions must not read alike.
func TestBuildDeploymentRecord_AVersionChangeChangesTheFingerprint(t *testing.T) {
	before := recordVariables(t,
		&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Version: 5},
	)
	after := recordVariables(t,
		&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Version: 6},
	)

	if before.ValueFingerprint == after.ValueFingerprint {
		t.Errorf("both fingerprint as %q, want a rotation to move it", before.ValueFingerprint)
	}
}

// The same variable set has to fingerprint the same however the manifest
// happened to order it, or an audit could not compare two deploys at all.
func TestBuildDeploymentRecord_TheFingerprintIsIndependentOfManifestOrder(t *testing.T) {
	plain := &deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2}
	live := &deploymentsv1.ManifestVariable{Key: "SESSION_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Folder: "/api", Version: 7}

	first := recordVariables(t, plain, live)
	second := recordVariables(t, live, plain)

	if first.ValueFingerprint != second.ValueFingerprint {
		t.Errorf("fingerprints = %q and %q, want one digest for one variable set", first.ValueFingerprint, second.ValueFingerprint)
	}
}

// A live value is whatever the store holds when the runtime fetches it, so
// recording the version the deploy happened to see would be the ledger
// claiming a reproducibility it cannot deliver.
func TestBuildDeploymentRecord_LiveKeysAreRecordedAsLatestAtRuntime(t *testing.T) {
	record := recordVariables(t,
		&deploymentsv1.ManifestVariable{Key: "SESSION_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Folder: "/api", Version: 7},
	)

	want := []edge.VariableRecord{{Key: "SESSION_SECRET", Folder: "/api", Live: true}}
	if !reflect.DeepEqual(record.Variables, want) {
		t.Errorf("Variables = %+v, want %+v — latest-at-runtime, never a version", record.Variables, want)
	}
}

// A preview keeps no audit ledger, so it records nothing — and recording
// nothing is the normal outcome, not a failure the deploy has to survive.
// Nothing else about the record moves with it: the same inputs built under
// either class differ in the two audit fields and nowhere else.
func TestBuildDeploymentRecord_PreviewRecordsNoVariablesAndIsNotAnError(t *testing.T) {
	manifest := varsManifest(
		&deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2},
	)
	id := fingerprinted("WEB1", "abc123")
	build := func(class deploymentsv1.Environment_Class) edge.DeploymentRecord {
		t.Helper()
		cfg := varsConfig(t, class)
		record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], id, nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord under %s: %v", class, err)
		}
		return record
	}

	preview := build(deploymentsv1.Environment_CLASS_PREVIEW)
	if preview.Variables != nil || preview.ValueFingerprint != "" {
		t.Errorf("preview record = %+v, want no audit fields at all", preview)
	}

	production := build(deploymentsv1.Environment_CLASS_PRODUCTION)
	production.Variables, production.ValueFingerprint = nil, ""
	// CreatedAt is stamped per call, so it is compared as "both stamped it"
	// rather than for equality.
	if preview.CreatedAt == 0 || production.CreatedAt == 0 {
		t.Errorf("CreatedAt = %d (preview) and %d (production), want both stamped", preview.CreatedAt, production.CreatedAt)
	}
	production.CreatedAt = preview.CreatedAt
	if !reflect.DeepEqual(preview, production) {
		t.Errorf("preview record = %+v, want the production one with only its audit fields dropped: %+v", preview, production)
	}
}

func TestBuildDeploymentRecord_AnAppWithNoVariablesRecordsNothing(t *testing.T) {
	record := recordVariables(t)

	if record.Variables != nil || record.ValueFingerprint != "" {
		t.Errorf("record = %+v, want nothing recorded rather than an empty list and a digest of nothing", record)
	}
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

// The image optimizer is account-global like the stores above and rides as a var
// for the same reason. A substrate that bootstrapped none must bind nothing at
// all: the worker reads an absent var as "no optimizer" and answers every valid
// /_next/image request 502, whereas a var bound to the empty string would be a
// URL it tries to POST to.
func TestRootStackSpecs_BindsImageOptimizerURL(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}

	t.Run("bound from the substrate's optimizer", func(t *testing.T) {
		url := "https://opt123.lambda-url.eu-west-1.on.aws/"
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1", ImageOptimizerURL: url}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if got := specs[0].Generic.Vars[edge.ImageOptimizerURLVar]; got != url {
			t.Errorf("Vars[%s] = %q, want %q", edge.ImageOptimizerURLVar, got, url)
		}
	})

	t.Run("absent on a substrate with no optimizer", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1"}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if _, ok := specs[0].Generic.Vars[edge.ImageOptimizerURLVar]; ok {
			t.Errorf("Vars[%s] must be unset, not bound empty", edge.ImageOptimizerURLVar)
		}
	})
}

// TestRootStackSpecs_BindsRevalidateQueueOnlyWithAConsumer is human decision F's
// deploy half, and it is a silent-failure guard rather than a tidiness one.
//
// Bootstrap publishes the queue URL only alongside a rendered revalidator, so an
// empty Config value means "this substrate has a queue that nothing drains".
// Binding it anyway is the epic's signature failure in miniature: the worker
// enqueues, SQS accepts, the refresh thunk reports landed, the colo sentinel
// re-arms as though the entry were fresh, and the route stops revalidating until
// it hard-expires — with nothing in the deploy or the logs saying so.
func TestRootStackSpecs_BindsRevalidateQueueOnlyWithAConsumer(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)
	manifest := &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}

	t.Run("bound when the substrate published a queue URL", func(t *testing.T) {
		url := "https://sqs.eu-west-1.amazonaws.com/1234/ocel-revalidate.fifo"
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1", RevalidateQueueURL: url}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if got := specs[0].Generic.Vars[edge.RevalidateQueueURLVar]; got != url {
			t.Errorf("Vars[%s] = %q, want %q", edge.RevalidateQueueURLVar, got, url)
		}
	})

	t.Run("absent where nothing drains the queue", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1"}
		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if _, ok := specs[0].Generic.Vars[edge.RevalidateQueueURLVar]; ok {
			t.Errorf("Vars[%s] must be unset, not bound empty: the edge would enqueue into a queue with no consumer and report the refresh landed", edge.RevalidateQueueURLVar)
		}
	})
}

func TestFinalizeProductionDeploy_ReconcileThenStageThenPromoteInOrder(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1", GenericName: "web-generic"}}
	results := []appDeployResult{
		{App: "web", Identity: buildOnly("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
		{App: "api", Identity: buildOnly("b2"), Record: edge.DeploymentRecord{App: "api", Identity: "b2"}},
	}

	state, err := finalizeDeploy(ctx, Config{}, fake, specs, nil, "promo1", "", "", 100, results)
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
		{App: "web", Identity: buildOnly("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
	}

	if _, err := finalizeDeploy(ctx, Config{}, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "v1.2.3", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}

	if len(fake.promotions) != 1 || fake.promotions[0].Tag != "v1.2.3" {
		t.Errorf("promotions = %v, want the promote to carry tag %q", fake.promotions, "v1.2.3")
	}
}

func TestFinalizeDeploy_PromotesTheGivenPointer(t *testing.T) {
	ctx := context.Background()
	results := []appDeployResult{
		{App: "web", Identity: buildOnly("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
	}

	prod := &recordingRootStack{}
	if _, err := finalizeDeploy(ctx, Config{}, prod, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy(production): %v", err)
	}
	if len(prod.promotePointers) != 1 || prod.promotePointers[0] != "" {
		t.Errorf("production promote pointer = %v, want the reserved default (empty)", prod.promotePointers)
	}

	preview := &recordingRootStack{}
	if _, err := finalizeDeploy(ctx, Config{}, preview, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "pr-42", 100, results); err != nil {
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
		{App: "web", Identity: buildOnly("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
	}

	if _, err := finalizeDeploy(ctx, Config{}, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
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
		{App: "web", Identity: buildOnly("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
		{App: "api", Err: errors.New("app-deploy stack failed")},
	}

	_, err := finalizeDeploy(ctx, Config{}, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results)
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
	results := []appDeployResult{{App: "web", Identity: buildOnly("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}}}

	state, err := finalizeDeploy(ctx, Config{}, fake, specs, nil, "promo1", "", "", 100, results)
	if err != nil {
		t.Fatalf("first finalizeDeploy: %v", err)
	}

	results2 := []appDeployResult{{App: "web", Identity: buildOnly("b2"), Record: edge.DeploymentRecord{App: "web", Identity: "b2"}}}
	if _, err := finalizeDeploy(ctx, Config{}, fake, specs, state, "promo2", "", "", 200, results2); err != nil {
		t.Fatalf("second finalizeDeploy: %v", err)
	}

	if len(fake.promotions) != 2 {
		t.Fatalf("promotions = %d, want 2 (both retained)", len(fake.promotions))
	}
	if fake.promotions[0].PromotionID != "promo1" || fake.promotions[1].PromotionID != "promo2" {
		t.Errorf("promotions = %+v, want promo1 then promo2", fake.promotions)
	}
}

// A rotation reuses the framework build, so both Deployments carry the same
// build id and only the value fingerprint tells them apart. The rotation still
// stages its own record and issues its own promotion, and the promotion the
// prior Deployment was made live by is left exactly as it was.
func TestFinalizeDeploy_RotationOfOneBuildIsANewDeploymentAndPromotion(t *testing.T) {
	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1"}}
	before, after := buildOnly("B1"), fingerprinted("B1", "fp2")

	result := func(id DeploymentIdentity) []appDeployResult {
		return []appDeployResult{{
			App:      "web",
			Identity: id,
			Record:   edge.DeploymentRecord{App: "web", Identity: id.String(), AssetPrefix: "assets/proj/web/B1"},
		}}
	}

	state, err := finalizeDeploy(ctx, Config{}, fake, specs, nil, "promo1", "", "", 100, result(before))
	if err != nil {
		t.Fatalf("first finalizeDeploy: %v", err)
	}
	if _, err := finalizeDeploy(ctx, Config{}, fake, specs, state, "promo2", "", "", 200, result(after)); err != nil {
		t.Fatalf("rotation finalizeDeploy: %v", err)
	}

	if len(fake.staged) != 2 {
		t.Fatalf("staged = %d records, want 2: a rotation is its own Deployment", len(fake.staged))
	}
	if got := []string{fake.staged[0].Identity, fake.staged[1].Identity}; got[0] != "B1" || got[1] != "B1~fp2" {
		t.Errorf("staged identities = %v, want [B1 B1~fp2]", got)
	}
	if len(fake.promotions) != 2 {
		t.Fatalf("promotions = %d, want 2: a rotation is its own promotion", len(fake.promotions))
	}
	if got := fake.promotions[1].Builds["web"]; got != "B1~fp2" {
		t.Errorf("rotation promotion Builds[web] = %q, want %q", got, "B1~fp2")
	}
	if got := fake.promotions[0].Builds["web"]; got != "B1" {
		t.Errorf("prior promotion Builds[web] = %q, want %q — the prior Deployment stays intact", got, "B1")
	}
	if a, b := AppDeployStackName("proj", "web", before), AppDeployStackName("proj", "web", after); a == b {
		t.Errorf("both Deployments name stack %q; a rotation must provision its own", a)
	}
}

// Rotating twice is three Deployments of one build, not a pair that keeps
// overwriting itself — and all three still address the same published bytes,
// which is what makes a rotation free of a framework rebuild.
func TestFinalizeDeploy_TwoRotationsOfOneBuildAreThreeDistinctDeployments(t *testing.T) {
	cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
	manifest := nextManifest()
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
	ids := []DeploymentIdentity{buildOnly("WEB1"), fingerprinted("WEB1", "fp2"), fingerprinted("WEB1", "fp3")}

	fake := &recordingRootStack{}
	ctx := context.Background()
	specs := []edge.RootStackSpec{{Version: "v1"}}
	var state edge.RootStackState
	for i, id := range ids {
		record, err := buildDeploymentRecord(cfg, manifest, app, id, nil)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		next, err := finalizeDeploy(ctx, originStore(cfg), fake, specs, state, fmt.Sprintf("promo%d", i+1), "", "", int64(100*(i+1)), []appDeployResult{{App: "web", Identity: id, Record: record}})
		if err != nil {
			t.Fatalf("finalizeDeploy %d: %v", i+1, err)
		}
		state = next
	}

	if len(fake.staged) != 3 || len(fake.promotions) != 3 {
		t.Fatalf("staged = %d records over %d promotions, want 3 and 3", len(fake.staged), len(fake.promotions))
	}
	wantIdentities := []string{"WEB1", "WEB1~fp2", "WEB1~fp3"}
	names := map[string]bool{}
	for i, want := range wantIdentities {
		if got := fake.staged[i].Identity; got != want {
			t.Errorf("staged[%d].Identity = %q, want %q", i, got, want)
		}
		if got := fake.promotions[i].Builds["web"]; got != want {
			t.Errorf("promotions[%d].Builds[web] = %q, want %q", i, got, want)
		}
		names[AppDeployStackName("proj", "web", ids[i])] = true
	}
	if len(names) != 3 {
		t.Errorf("app-deploy stack names = %v, want three distinct ones", names)
	}

	for i, record := range fake.staged {
		if want := "assets/proj/web/WEB1"; record.AssetPrefix != want {
			t.Errorf("staged[%d].AssetPrefix = %q, want %q on every rotation", i, record.AssetPrefix, want)
		}
		if want := "prod/proj/web/WEB1"; record.IsrPrefix != want {
			t.Errorf("staged[%d].IsrPrefix = %q, want %q on every rotation", i, record.IsrPrefix, want)
		}
		if want := "edge/proj/web/WEB1/bundle.json"; record.EdgeWorkers.BundleKey != want {
			t.Errorf("staged[%d].EdgeWorkers.BundleKey = %q, want %q on every rotation", i, record.EdgeWorkers.BundleKey, want)
		}
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

// The identity is derived in exactly one place, and today it is the framework
// build id with no fingerprint — so a Deployment's identity is byte-identical to
// the build id it came from.
func TestAssignIdentities_NextAppTakesItsBuildIDWithNoFingerprint(t *testing.T) {
	cfg := Config{ArtifactRoot: writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})}
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: frameworkNext}},
	}

	ids, err := assignIdentities(cfg, manifest, nil)
	if err != nil {
		t.Fatalf("assignIdentities: %v", err)
	}
	if got, want := ids["web"], buildOnly("WEB1"); got != want {
		t.Errorf("identities[web] = %+v, want %+v", got, want)
	}
	if got := ids["web"].String(); got != "WEB1" {
		t.Errorf("rendered identity = %q, want the bare build id %q", got, "WEB1")
	}
}

func TestAssignIdentities_FrameworkWithNoBuildIDGetsAMintedOne(t *testing.T) {
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "api", Framework: "express"}},
	}

	ids, err := assignIdentities(Config{ArtifactRoot: t.TempDir()}, manifest, nil)
	if err != nil {
		t.Fatalf("assignIdentities: %v", err)
	}
	if ids["api"].BuildID() == "" {
		t.Error("identities[api] carries no build id")
	}
	if ids["api"].Fingerprint() != "" {
		t.Errorf("Fingerprint = %q, want empty: nothing is baked yet", ids["api"].Fingerprint())
	}
}

// TestAssignIdentities_BakedValuesFingerprintTheIdentity is what lets a
// rotation exist at all: the build id is unchanged by a vars-only deploy, so
// the values the Deployment bakes are the only thing that can tell it from the
// Deployment it replaces.
func TestAssignIdentities_BakedValuesFingerprintTheIdentity(t *testing.T) {
	cfg := Config{ArtifactRoot: writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})}
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: frameworkNext}},
	}

	ids, err := assignIdentities(cfg, manifest, map[string]appBundle{"web": {Fingerprint: "abc123"}})
	if err != nil {
		t.Fatalf("assignIdentities: %v", err)
	}
	if got, want := ids["web"].String(), "WEB1~abc123"; got != want {
		t.Errorf("rendered identity = %q, want %q", got, want)
	}
}

// TestAssignIdentities_NothingBakedStaysTheBareBuildID holds the line that
// makes fingerprints free for everyone who bakes nothing: their records, stack
// names and promotions are byte-for-byte what they were before.
func TestAssignIdentities_NothingBakedStaysTheBareBuildID(t *testing.T) {
	cfg := Config{ArtifactRoot: writeTree(t, map[string]string{
		"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
	})}
	manifest := &deploymentsv1.Manifest{
		Slug: "proj",
		Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: frameworkNext}},
	}

	ids, err := assignIdentities(cfg, manifest, map[string]appBundle{"web": {Live: []byte("{}")}})
	if err != nil {
		t.Fatalf("assignIdentities: %v", err)
	}
	if got, want := ids["web"].String(), "WEB1"; got != want {
		t.Errorf("rendered identity = %q, want the bare build id %q", got, want)
	}
}

// The record is keyed by the identity, but the bytes the build published are
// keyed by the build id alone — two Deployments of one build share them.
func TestBuildDeploymentRecord_IdentityKeysTheRecordAndTheBuildKeysTheBytes(t *testing.T) {
	cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
	manifest := nextManifest()
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
	id := fingerprinted("WEB1", "fp1")

	record, err := buildDeploymentRecord(cfg, manifest, app, id, nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	if record.Identity != id.String() {
		t.Errorf("Identity = %q, want %q", record.Identity, id.String())
	}
	if want := "assets/proj/web/WEB1"; record.AssetPrefix != want {
		t.Errorf("AssetPrefix = %q, want %q — static assets stay keyed by the build id", record.AssetPrefix, want)
	}
	if want := "prod/proj/web/WEB1"; record.IsrPrefix != want {
		t.Errorf("IsrPrefix = %q, want %q", record.IsrPrefix, want)
	}
	if want := "edge/proj/web/WEB1/bundle.json"; record.EdgeWorkers.BundleKey != want {
		t.Errorf("BundleKey = %q, want %q — two Deployments of one build share an edge bundle", record.EdgeWorkers.BundleKey, want)
	}
}

// The record's wire name for its key predates identities and stays as it is:
// the store and the frozen worker read this JSON.
func TestBuildDeploymentRecord_IdentityIsWiredAsBuildId(t *testing.T) {
	cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
	app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
	id := fingerprinted("WEB1", "fp1")

	record, err := buildDeploymentRecord(cfg, nextManifest(), app, id, nil)
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if wire["buildId"] != id.String() {
		t.Errorf("record JSON buildId = %v, want %q", wire["buildId"], id.String())
	}
}

// The promotion's per-app entry is what the store resolves a record by, so it
// must be the identity, not the build both Deployments of a rotation share.
func TestFinalizeDeploy_PromotionCarriesRenderedIdentities(t *testing.T) {
	fake := &recordingRootStack{}
	id := fingerprinted("b1", "fp1")
	results := []appDeployResult{
		{App: "web", Identity: id, Record: edge.DeploymentRecord{App: "web", Identity: id.String()}},
	}

	if _, err := finalizeDeploy(context.Background(), Config{}, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
		t.Fatalf("finalizeDeploy: %v", err)
	}
	if len(fake.promotions) != 1 {
		t.Fatalf("promotions = %d, want 1", len(fake.promotions))
	}
	if got := fake.promotions[0].Builds["web"]; got != id.String() {
		t.Errorf("promotion.Builds[web] = %q, want %q", got, id.String())
	}
}
