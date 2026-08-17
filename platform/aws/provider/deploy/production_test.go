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
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func setStoreWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.KindBundleManifest{edge.KindCloudflare: bundle})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.EnvStoreWorkerBundles, string(raw))
}

func TestRootStackSpecs(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)

	webManifest := func() *deploymentsv1.Manifest {
		return &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		}
	}

	t.Run("threads edge values with no worker-fronted apps", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, EdgeValues: map[string]string{"cacheBucket": "ocel-proj-cache"}}
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

	t.Run("threads edge values with a worker-fronted app", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, EdgeValues: map[string]string{"cacheBucket": "ocel-proj-cache"}}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
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

	t.Run("production prunes stale routes", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Class: deploymentsv1.Environment_CLASS_PRODUCTION, ArtifactRoot: t.TempDir()}

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
		if specs[0].PruneWorkerStem != "" {
			t.Errorf("PruneWorkerStem = %q, want empty: a production spec sweeps its own script alone", specs[0].PruneWorkerStem)
		}
	})

	t.Run("preview with no declared domain is refused", func(t *testing.T) {
		manifest := webManifest()
		cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42", ArtifactRoot: specsArtifactRoot(t, manifest)}

		_, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err == nil {
			t.Fatal("expected a preview deploy with no declared preview domain to be refused")
		}
		if !strings.Contains(err.Error(), "domains.preview") {
			t.Errorf("error must point at the config field to add, got %q", err)
		}
	})

	t.Run("preview without a wildcard fails the deploy", func(t *testing.T) {
		manifest := webManifest()
		manifest.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"app.acme.com"}}}
		cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42", ArtifactRoot: specsArtifactRoot(t, manifest)}

		_, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err == nil {
			t.Fatal("expected a preview whose declared domain is not a wildcard to fail the deploy")
		}
		if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "app.acme.com") {
			t.Errorf("error must name the app and the offending domain, got %q", err)
		}
	})

	t.Run("preview is one project-scoped spec", func(t *testing.T) {
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
			Edge:         &recordingEdge{},
			Slug:         "proj",
			Class:        deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:     "pr-42",
			ArtifactRoot: specsArtifactRoot(t, manifest),
		}

		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1 for the whole project", len(specs))
		}
		spec := specs[0]
		if spec.GenericName != previewWorkerName("proj") {
			t.Errorf("GenericName = %q, want %q", spec.GenericName, previewWorkerName("proj"))
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
		if spec.PruneWorkerStem != previewWorkerStem("proj") {
			t.Errorf("PruneWorkerStem = %q, want %q", spec.PruneWorkerStem, previewWorkerStem("proj"))
		}
		if spec.Generic.Vars[envPreview] != "1" {
			t.Errorf("Vars[%s] = %q, want 1", envPreview, spec.Generic.Vars[envPreview])
		}
		if spec.Generic.Vars[envPreviewBaseDomain] != "preview.acme.com" {
			t.Errorf("Vars[%s] = %q, want preview.acme.com", envPreviewBaseDomain, spec.Generic.Vars[envPreviewBaseDomain])
		}
		if app, ok := spec.Generic.Vars["OCEL_APP"]; ok {
			t.Errorf("Vars[OCEL_APP] = %q, want it unset for preview", app)
		}
		if got := spec.Generic.Vars[envPreviewApps]; got != "web,api" {
			t.Errorf("Vars[%s] = %q, want web,api", envPreviewApps, got)
		}
	})

	t.Run("preview always binds the app list", func(t *testing.T) {
		manifest := &deploymentsv1.Manifest{Slug: "proj"}
		cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42", ArtifactRoot: specsArtifactRoot(t, manifest)}

		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if _, ok := specs[0].Generic.Vars[envPreviewApps]; !ok {
			t.Errorf("Vars = %v, want %s bound even with no app to name", specs[0].Generic.Vars, envPreviewApps)
		}
	})

	t.Run("every worker declares Lambda's invoke-payload budget to the edge", func(t *testing.T) {
		manifest := webManifest()
		cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PRODUCTION, ArtifactRoot: specsArtifactRoot(t, manifest)}

		specs, err := rootStackSpecs(cfg, manifest, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		for _, spec := range specs {
			if got := spec.Generic.Vars[edge.OriginBodyLimitVar]; got != "6289408" {
				t.Errorf("Vars[%s] = %q, want 6289408", edge.OriginBodyLimitVar, got)
			}
			if got := spec.Generic.Vars[edge.OriginBodyEncodingVar]; got != edge.OriginBodyEncodingBase64 {
				t.Errorf("Vars[%s] = %q, want %q", edge.OriginBodyEncodingVar, got, edge.OriginBodyEncodingBase64)
			}
		}
	})

	t.Run("production keeps declarative hostnames", func(t *testing.T) {
		manifest := webManifest()
		manifest.Domains = map[string]*deploymentsv1.DomainList{"production": {Hostnames: []string{"acme.com", "www.acme.com"}}}
		cfg := Config{Edge: &recordingEdge{}, Slug: "proj", Class: deploymentsv1.Environment_CLASS_PRODUCTION, ArtifactRoot: specsArtifactRoot(t, manifest)}

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
	})

	t.Run("binds edge signing credentials when the substrate has them", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret-edge"}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
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

	t.Run("edge signing credentials are absent on a substrate predating them", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
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

	t.Run("binds cache coordinates from the substrate's stores", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1", StateTable: "state-abc", AssetBucket: "assets-xyz"}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
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

	t.Run("cache coordinates are absent on a substrate predating a store", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1"}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
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

	t.Run("binds the image optimizer URL from the substrate's optimizer", func(t *testing.T) {
		url := "https://opt123.lambda-url.eu-west-1.on.aws/"
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1", ImageOptimizerURL: url}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if got := specs[0].Generic.Vars[edge.ImageOptimizerURLVar]; got != url {
			t.Errorf("Vars[%s] = %q, want %q", edge.ImageOptimizerURLVar, got, url)
		}
	})

	t.Run("the image optimizer URL is absent on a substrate with no optimizer", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1"}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if _, ok := specs[0].Generic.Vars[edge.ImageOptimizerURLVar]; ok {
			t.Errorf("Vars[%s] must be unset, not bound empty", edge.ImageOptimizerURLVar)
		}
	})

	t.Run("binds the revalidate queue URL when the substrate published one", func(t *testing.T) {
		url := "https://sqs.eu-west-1.amazonaws.com/1234/ocel-revalidate.fifo"
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1", RevalidateQueueURL: url}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if got := specs[0].Generic.Vars[edge.RevalidateQueueURLVar]; got != url {
			t.Errorf("Vars[%s] = %q, want %q", edge.RevalidateQueueURLVar, got, url)
		}
	})

	t.Run("the revalidate queue URL is absent where nothing drains the queue", func(t *testing.T) {
		cfg := Config{Edge: &recordingEdge{}, Region: "eu-west-1"}
		specs, err := rootStackSpecs(cfg, webManifest(), "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if _, ok := specs[0].Generic.Vars[edge.RevalidateQueueURLVar]; ok {
			t.Errorf("Vars[%s] must be unset, not bound empty: the edge would enqueue into a queue with no consumer and report the refresh landed", edge.RevalidateQueueURLVar)
		}
	})
}

func TestPreviewBaseDomain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a wildcard over a preview subdomain", "*.preview.acme.com", "preview.acme.com"},
		{"a wildcard over the apex", "*.acme.com", "acme.com"},
		{"a plain hostname has no base", "acme.com", ""},
		{"nothing declared", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := previewBaseDomain(tc.in); got != tc.want {
				t.Errorf("previewBaseDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPreviewWildcard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a base domain gains the wildcard label", "preview.acme.com", "*.preview.acme.com"},
		{"nothing declared", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := previewWildcard(tc.in); got != tc.want {
				t.Errorf("previewWildcard(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPreviewHostnames(t *testing.T) {
	t.Parallel()

	t.Run("resolves the wildcard to the pointer hosts, a single app elides the app label", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
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

	t.Run("resolves the wildcard to the pointer hosts, two apps qualify the label", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
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

	t.Run("serves an app that declares nothing under the project wildcard", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Slug: "proj", Identity: "pr-42"}
		apps := []*deploymentsv1.ManifestApp{{Name: "web"}, {Name: "api"}}

		got, err := previewHostnames(cfg, apps, map[string][]string{"web": {"*.preview.acme.com"}})
		if err != nil {
			t.Fatalf("previewHostnames: %v", err)
		}
		if want := []string{"pr-42--api.preview.acme.com"}; !slicesEqual(got.hosts["api"], want) {
			t.Errorf("api hosts = %v, want %v", got.hosts["api"], want)
		}
	})

	t.Run("two preview domains in one project are refused", func(t *testing.T) {
		t.Parallel()
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
	})

}

func TestResolveWorkerHostnames(t *testing.T) {
	t.Parallel()

	t.Run("production serves its declared hostnames", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
			Domains:   map[string]*deploymentsv1.DomainList{"production": {Hostnames: []string{"acme.com", "www.acme.com"}}},
		}
		artifactRoot := t.TempDir()
		writeRoutingManifest(t, artifactRoot, "web", `{"buildId":"b1"}`)
		cfg := Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION, Slug: "proj", ArtifactRoot: artifactRoot}

		resolved, err := resolveWorkerHostnames(cfg, manifest, workerApps(artifactRoot, manifest))
		if err != nil {
			t.Fatalf("resolveWorkerHostnames: %v", err)
		}
		if want := []string{"acme.com", "www.acme.com"}; !slicesEqual(resolved.hosts["web"], want) {
			t.Errorf("hostnames = %v, want the declared hostnames %v", resolved.hosts["web"], want)
		}
	})
}

func TestAmbientPreview(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)

	manifest := func(apps ...string) *deploymentsv1.Manifest {
		m := &deploymentsv1.Manifest{Slug: "proj"}
		for _, app := range apps {
			m.Apps = append(m.Apps, &deploymentsv1.ManifestApp{Name: app, Framework: "next"})
			m.Functions = append(m.Functions, &deploymentsv1.ManifestFunction{
				LogicalName: app + "_index", Framework: "next", App: app, RouteId: "/",
			})
		}
		return m
	}
	ambient := func(t *testing.T, m *deploymentsv1.Manifest) Config {
		return Config{
			Edge:                &recordingEdge{},
			Slug:                "proj",
			Class:               deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:            "pr-42",
			GlobalPreviewDomain: "preview.acme.com",
			ArtifactRoot:        specsArtifactRoot(t, m),
		}
	}

	t.Run("a first ambient deploy still writes the project's root-stack state", func(t *testing.T) {
		m := manifest("web")
		specs, err := rootStackSpecs(ambient(t, m), m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}

		fake := &recordingRootStack{}
		state, err := reconcileRootStack(context.Background(), fake, specs, nil)
		if err != nil {
			t.Fatalf("reconcileRootStack: %v", err)
		}
		if state[edge.RootStackKeyEndpoint] == "" {
			t.Fatalf("state = %v, want a store endpoint: staging a deployment reads it", state)
		}
		if state[edge.RootStackKeySlug] != "proj" {
			t.Errorf("state[%s] = %q, want proj: `ocel domain ls` and preview pruning find the project by it", edge.RootStackKeySlug, state[edge.RootStackKeySlug])
		}
	})

	t.Run("an ambient deploy claims no hostname of its own", func(t *testing.T) {
		m := manifest("web")
		specs, err := rootStackSpecs(ambient(t, m), m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
		spec := specs[0]
		if len(spec.Domains) != 0 {
			t.Errorf("Domains = %v, want none: the shared entry worker serves the global wildcard", spec.Domains)
		}
		if got, ok := spec.Generic.Vars[envPreviewBaseDomain]; ok {
			t.Errorf("Vars[%s] = %q, want unset: no per-project worker answers on the global domain", envPreviewBaseDomain, got)
		}
	})

	t.Run("dropping a declared preview domain prunes the worker it left behind", func(t *testing.T) {
		m := manifest("web")
		specs, err := rootStackSpecs(ambient(t, m), m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		spec := specs[0]
		if spec.GenericName != previewWorkerName("proj") {
			t.Errorf("GenericName = %q, want %q", spec.GenericName, previewWorkerName("proj"))
		}
		if !spec.PruneRoutes {
			t.Error("PruneRoutes = false: the old wildcard route must stop serving the last promoted build")
		}
		if spec.PruneWorkerStem != previewWorkerStem("proj") {
			t.Errorf("PruneWorkerStem = %q, want %q", spec.PruneWorkerStem, previewWorkerStem("proj"))
		}
		if !spec.PruneOnly {
			t.Error("PruneOnly = false: an ambient project must not upload or expose a per-project preview worker")
		}
	})

	t.Run("a preview on its own domain still ships a worker", func(t *testing.T) {
		m := manifest("web")
		m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
		specs, err := rootStackSpecs(ambient(t, m), m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if specs[0].PruneOnly {
			t.Error("PruneOnly = true: a project serving its own preview wildcard needs its worker")
		}
	})

	t.Run("a preview with no apps exposes nothing, on any domain", func(t *testing.T) {
		for _, base := range []string{"preview.acme.com", ""} {
			m := manifest()
			m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
			cfg := ambient(t, m)
			cfg.GlobalPreviewDomain = base
			specs, err := rootStackSpecs(cfg, m, "v1", nil)
			if err != nil {
				t.Fatalf("rootStackSpecs: %v", err)
			}
			if len(specs) != 1 {
				t.Fatalf("specs = %d, want 1", len(specs))
			}
			if !specs[0].PruneOnly {
				t.Errorf("GlobalPreviewDomain %q: PruneOnly = false: a worker with no app answers nothing, so it must not be uploaded or exposed", base)
			}
		}
	})

	t.Run("app URLs land on the global preview domain", func(t *testing.T) {
		cases := []struct {
			name string
			apps []string
			want []string
		}{
			{"a single app elides the app label", []string{"web"}, []string{"https://proj--pr-42.preview.acme.com"}},
			{"two apps qualify the label", []string{"web", "api"}, []string{
				"https://proj--pr-42--web.preview.acme.com",
				"https://proj--pr-42--api.preview.acme.com",
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				m := manifest(tc.apps...)
				outs := workerURLOutputs(ambient(t, m), m)
				if got := appURLs(m, outs); !slicesEqual(got, tc.want) {
					t.Errorf("AppURLs = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("the deploy marks the project as served on the global domain", func(t *testing.T) {
		m := manifest("web")
		cfg := ambient(t, m)
		specs, err := rootStackSpecs(cfg, m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}

		fake := &recordingRootStack{}
		state, err := reconcileRootStack(context.Background(), fake, specs, edge.RootStackState{
			edge.RootStackKeyGlobalPreview: cfg.GlobalPreviewDomain,
		})
		if err != nil {
			t.Fatalf("reconcileRootStack: %v", err)
		}
		if edge.ServedOnGlobalPreview(state, cfg.GlobalPreviewDomain) {
			t.Fatal("the reconciled state kept the prior mark; stamping it before the reconcile would then be enough, and this test proves nothing")
		}

		state = MarkGlobalPreview(state, cfg, m)
		if !edge.ServedOnGlobalPreview(state, cfg.GlobalPreviewDomain) {
			t.Errorf("state = %v, want the global preview domain stamped: `ocel domain ls` lists the projects by it", state)
		}
	})

	t.Run("declaring its own preview domain clears the mark", func(t *testing.T) {
		m := manifest("web")
		cfg := ambient(t, m)
		m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
		specs, err := rootStackSpecs(cfg, m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}

		fake := &recordingRootStack{version: "v1", secret: "fake-secret"}
		prior := edge.RootStackState{
			edge.RootStackKeySlug:          "proj",
			edge.RootStackKeyEndpoint:      fakeStoreEndpoint,
			edge.RootStackKeySecret:        "fake-secret",
			edge.RootStackKeyGlobalPreview: cfg.GlobalPreviewDomain,
		}
		state, err := reconcileRootStack(context.Background(), fake, specs, prior)
		if err != nil {
			t.Fatalf("reconcileRootStack: %v", err)
		}

		state = MarkGlobalPreview(state, cfg, m)
		if edge.ServedOnGlobalPreview(state, cfg.GlobalPreviewDomain) {
			t.Errorf("state = %v, want the mark cleared: this project serves its own wildcard now", state)
		}
	})

	t.Run("an over-long label is refused at deploy time", func(t *testing.T) {
		pointer := strings.Repeat("p", previewLabelMaxLen)

		t.Run("on the global domain, where the slug is part of the label", func(t *testing.T) {
			m := manifest("web")
			cfg := ambient(t, m)
			cfg.Slug = "acme"
			cfg.Identity = pointer

			_, err := rootStackSpecs(cfg, m, "v1", nil)
			if err == nil {
				t.Fatalf("expected a %d-character label to be refused", len("acme--")+len(pointer))
			}
			for _, want := range []string{"acme", pointer, "69", "63", "6"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q, got %q", want, err)
				}
			}
		})

		t.Run("on the project's own wildcard", func(t *testing.T) {
			m := manifest("web")
			m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
			cfg := ambient(t, m)
			cfg.Identity = pointer + "pppppp"

			_, err := rootStackSpecs(cfg, m, "v1", nil)
			if err == nil {
				t.Fatalf("expected a %d-character label to be refused", len(cfg.Identity))
			}
			for _, want := range []string{cfg.Identity, "69", "63", "6"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must name %q, got %q", want, err)
				}
			}
		})
	})

	t.Run("a project that declares its own preview domain ignores the global one", func(t *testing.T) {
		m := manifest("web")
		m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
		cfg := ambient(t, m)

		specs, err := rootStackSpecs(cfg, m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if !slicesEqual(specs[0].Domains, []string{"*.preview.proj.com"}) {
			t.Errorf("Domains = %v, want the project's own wildcard", specs[0].Domains)
		}
		if got := appURLs(m, workerURLOutputs(cfg, m)); !slicesEqual(got, []string{"https://pr-42.preview.proj.com"}) {
			t.Errorf("AppURLs = %v, want the project's own wildcard resolved", got)
		}
	})
}

func TestWorkerAppURL(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			if got := workerAppURL(tc.domains); got != tc.want {
				t.Errorf("workerAppURL(%v) = %q, want %q", tc.domains, got, tc.want)
			}
		})
	}
}

func TestPreviewAppNames(t *testing.T) {
	t.Parallel()

	t.Run("is a lowercased comma separated list", func(t *testing.T) {
		t.Parallel()
		got := previewAppNames([]*deploymentsv1.ManifestApp{{Name: "Web"}, {Name: " admin "}, {Name: ""}})
		if got != "web,admin" {
			t.Errorf("previewAppNames = %q, want web,admin", got)
		}
	})

	t.Run("no app names nothing", func(t *testing.T) {
		t.Parallel()
		if got := previewAppNames(nil); got != "" {
			t.Errorf("previewAppNames(nil) = %q, want empty", got)
		}
	})
}

func varsManifest(variables ...*deploymentsv1.ManifestVariable) *deploymentsv1.Manifest {
	return &deploymentsv1.Manifest{
		Slug:      "proj",
		Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next", Variables: variables}},
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
	}
}

func varsConfig(t *testing.T, class deploymentsv1.Environment_Class) Config {
	t.Helper()
	return Config{
		ArtifactRoot: writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`}),
		Slug:         "proj",
		Class:        class,
	}
}

func recordVariables(t *testing.T, variables ...*deploymentsv1.ManifestVariable) edge.DeploymentRecord {
	t.Helper()
	manifest := varsManifest(variables...)
	cfg := varsConfig(t, deploymentsv1.Environment_CLASS_PRODUCTION)
	record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest))
	if err != nil {
		t.Fatalf("buildDeploymentRecord: %v", err)
	}
	return record
}

func TestBuildDeploymentRecord(t *testing.T) {
	t.Parallel()

	t.Run("carries no route hostnames", func(t *testing.T) {
		t.Parallel()
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

		record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest))
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
	})

	t.Run("carries the framework", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}, {Name: "docs"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		}
		cfg := Config{
			ArtifactRoot: writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`}),
			Slug:         "proj",
		}

		record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], deployedAs("WEB1"), nil, appBuildsFor(t, cfg, manifest))
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if record.Framework != "next" {
			t.Errorf("Framework = %q, want next", record.Framework)
		}
	})

	t.Run("carries an empty framework for an app that declares none", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}, {Name: "docs"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		}
		cfg := Config{
			ArtifactRoot: writeTree(t, map[string]string{"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`}),
			Slug:         "proj",
		}

		bare, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[1], deployedAs("DOCS1"), nil, appBuildsFor(t, cfg, manifest))
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
	})

	t.Run("production carries the fingerprint of what it shipped", func(t *testing.T) {
		t.Parallel()
		record := recordVariables(t,
			&deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2},
			&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Folder: "/admin", Version: 5},
		)

		if record.ValueFingerprint == "" {
			t.Errorf("record = %+v, want a fingerprint of what it shipped", record)
		}
	})

	t.Run("production carries per key versions", func(t *testing.T) {
		t.Parallel()
		record := recordVariables(t,
			&deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2},
			&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Folder: "/admin", Version: 5},
		)

		want := []edge.VariableRecord{
			{Key: "API_KEY", Folder: "/admin", Version: 5},
			{Key: "POSTHOG_ID", Version: 2},
		}
		if !reflect.DeepEqual(record.Variables, want) {
			t.Errorf("Variables = %+v, want %+v sorted by key", record.Variables, want)
		}
	})

	t.Run("an all live app still fingerprints what it shipped", func(t *testing.T) {
		t.Parallel()
		record := recordVariables(t,
			&deploymentsv1.ManifestVariable{Key: "SESSION_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Version: 7},
		)

		if record.ValueFingerprint == "" {
			t.Errorf("record = %+v, want a fingerprint even though nothing was baked", record)
		}
	})

	t.Run("a version change changes the fingerprint", func(t *testing.T) {
		t.Parallel()
		before := recordVariables(t,
			&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Version: 5},
		)
		after := recordVariables(t,
			&deploymentsv1.ManifestVariable{Key: "API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Version: 6},
		)

		if before.ValueFingerprint == after.ValueFingerprint {
			t.Errorf("both fingerprint as %q, want a rotation to move it", before.ValueFingerprint)
		}
	})

	t.Run("the fingerprint is independent of manifest order", func(t *testing.T) {
		t.Parallel()
		plain := &deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2}
		live := &deploymentsv1.ManifestVariable{Key: "SESSION_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Folder: "/api", Version: 7}

		first := recordVariables(t, plain, live)
		second := recordVariables(t, live, plain)

		if first.ValueFingerprint != second.ValueFingerprint {
			t.Errorf("fingerprints = %q and %q, want one digest for one variable set", first.ValueFingerprint, second.ValueFingerprint)
		}
	})

	t.Run("live keys are recorded as latest at runtime", func(t *testing.T) {
		t.Parallel()
		record := recordVariables(t,
			&deploymentsv1.ManifestVariable{Key: "SESSION_SECRET", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Folder: "/api", Version: 7},
		)

		want := []edge.VariableRecord{{Key: "SESSION_SECRET", Folder: "/api", Live: true}}
		if !reflect.DeepEqual(record.Variables, want) {
			t.Errorf("Variables = %+v, want %+v — latest-at-runtime, never a version", record.Variables, want)
		}
	})

	t.Run("preview records no variables and is not an error", func(t *testing.T) {
		t.Parallel()
		manifest := varsManifest(
			&deploymentsv1.ManifestVariable{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Version: 2},
		)
		id := fingerprinted("WEB1", "abc123")
		build := func(class deploymentsv1.Environment_Class) edge.DeploymentRecord {
			t.Helper()
			cfg := varsConfig(t, class)
			record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], id, nil, appBuildsFor(t, cfg, manifest))
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
		if preview.CreatedAt == 0 || production.CreatedAt == 0 {
			t.Errorf("CreatedAt = %d (preview) and %d (production), want both stamped", preview.CreatedAt, production.CreatedAt)
		}
		production.CreatedAt = preview.CreatedAt
		if !reflect.DeepEqual(preview, production) {
			t.Errorf("preview record = %+v, want the production one with only its audit fields dropped: %+v", preview, production)
		}
	})

	t.Run("an app with no variables records nothing", func(t *testing.T) {
		t.Parallel()
		record := recordVariables(t)

		if record.Variables != nil || record.ValueFingerprint != "" {
			t.Errorf("record = %+v, want nothing recorded rather than an empty list and a digest of nothing", record)
		}
	})

	t.Run("the identity keys the record", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
		manifest := nextManifest()
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
		id := fingerprinted("WEB1", "fp1")

		record, err := buildDeploymentRecord(cfg, manifest, app, id, nil, appBuildsFor(t, cfg, manifest))
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if record.Identity != id.String() {
			t.Errorf("Identity = %q, want %q", record.Identity, id.String())
		}
	})

	t.Run("the release keys the bytes", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
		manifest := nextManifest()
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
		builds := releaseBuilds(t, cfg, manifest, "fp1")
		id := builds.identities["web"]

		record, err := buildDeploymentRecord(cfg, manifest, app, id, nil, builds)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		root := "prod/proj/web/" + releaseOf(id).String() + "/"
		if want := root + "assets"; record.AssetPrefix != want {
			t.Errorf("AssetPrefix = %q, want %q", record.AssetPrefix, want)
		}
		if want := root + "isr"; record.IsrPrefix != want {
			t.Errorf("IsrPrefix = %q, want %q", record.IsrPrefix, want)
		}
		if want := root + "edge/bundle.json"; record.EdgeWorkers.BundleKey != want {
			t.Errorf("BundleKey = %q, want %q — one release, one prefix", record.EdgeWorkers.BundleKey, want)
		}
	})

	t.Run("a node framework app records its origin", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: nodeAppTree(t), Env: "prod", Slug: "proj", Edge: testLoaderEdge()}
		manifest := nodeManifest()
		builds := appBuildsFor(t, cfg, manifest)
		id := builds.identities["api"]
		outs := []*deploymentsv1.FunctionOutput{fnOutput("api_handler", "https://api-fn.lambda-url.aws/")}

		record, err := buildDeploymentRecord(cfg, manifest, manifest.GetApps()[0], id, outs, builds)
		if err != nil {
			t.Fatalf("buildDeploymentRecord: %v", err)
		}
		if record.App != "api" || record.Framework != "express" {
			t.Errorf("record = %+v, want the app and its framework", record)
		}
		if record.Identity != id.String() {
			t.Errorf("Identity = %q, want %q", record.Identity, id.String())
		}
		if got := record.FunctionURLs["/"]; got != "https://api-fn.lambda-url.aws/" {
			t.Errorf("FunctionURLs = %v, want the origin the worker signs for", record.FunctionURLs)
		}
		if record.RoutingManifest != nil {
			t.Errorf("RoutingManifest = %v, want none from a build that emitted none", record.RoutingManifest)
		}
		if record.AssetPrefix != "" || record.IsrPrefix != "" || record.EdgeWorkers != nil {
			t.Errorf("record = %+v, want no next-only fields", record)
		}

		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		for _, key := range []string{"framework", "buildId", "functionUrls"} {
			if _, ok := wire[key]; !ok {
				t.Errorf("record JSON %s is missing %q", raw, key)
			}
		}
	})

	t.Run("the identity keys the record, the deployment and the build ride along", func(t *testing.T) {
		t.Parallel()
		cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
		id := fingerprinted("dep1", "fp1")

		record, err := buildDeploymentRecord(cfg, nextManifest(), app, id, nil, appBuildsFor(t, cfg, nextManifest()))
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
		if wire["identity"] != id.String() {
			t.Errorf("record JSON identity = %v, want %q", wire["identity"], id.String())
		}
		if wire["deploymentId"] != id.DeploymentID() {
			t.Errorf("record JSON deploymentId = %v, want the bare deployment id", wire["deploymentId"])
		}
		if wire["buildId"] != "WEB1" {
			t.Errorf("record JSON buildId = %v, want the framework's own build id", wire["buildId"])
		}
	})
}

func TestFinalizeProductionDeploy(t *testing.T) {
	t.Run("reconcile then stage then promote in order", func(t *testing.T) {
		fake := &recordingRootStack{}
		ctx := context.Background()
		specs := []edge.RootStackSpec{{Version: "v1", GenericName: "web-generic"}}
		results := []appDeployResult{
			{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
			{App: "api", Identity: deployedAs("b2"), Record: edge.DeploymentRecord{App: "api", Identity: "b2"}},
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
		want := map[string]string{"web": deployedAs("b1").String(), "api": deployedAs("b2").String()}
		if len(fake.promotions[0].Builds) != len(want) {
			t.Fatalf("promotion builds = %v, want %v", fake.promotions[0].Builds, want)
		}
		for app, identity := range want {
			if got := fake.promotions[0].Builds[app]; got != identity {
				t.Errorf("promotion.Builds[%q] = %q, want %q", app, got, identity)
			}
		}
		if state[edge.RootStackKeyEndpoint] == "" {
			t.Error("expected a reconciled state to be returned")
		}
	})

	t.Run("stamps the tag onto the promotion", func(t *testing.T) {
		fake := &recordingRootStack{}
		ctx := context.Background()
		results := []appDeployResult{
			{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
		}

		if _, err := finalizeDeploy(ctx, Config{}, fake, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "v1.2.3", "", 100, results); err != nil {
			t.Fatalf("finalizeDeploy: %v", err)
		}

		if len(fake.promotions) != 1 || fake.promotions[0].Tag != "v1.2.3" {
			t.Errorf("promotions = %v, want the promote to carry tag %q", fake.promotions, "v1.2.3")
		}
	})

	t.Run("stages before any promote", func(t *testing.T) {
		fake := &orderTrackingRootStack{recordingRootStack: &recordingRootStack{}}
		ctx := context.Background()
		results := []appDeployResult{
			{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
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
	})

	t.Run("an app failure aborts the promote", func(t *testing.T) {
		fake := &recordingRootStack{}
		ctx := context.Background()
		results := []appDeployResult{
			{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
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
	})

	t.Run("a second deploy produces a new promotion retaining the prior one", func(t *testing.T) {
		fake := &recordingRootStack{}
		ctx := context.Background()
		specs := []edge.RootStackSpec{{Version: "v1"}}
		results := []appDeployResult{{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}}}

		state, err := finalizeDeploy(ctx, Config{}, fake, specs, nil, "promo1", "", "", 100, results)
		if err != nil {
			t.Fatalf("first finalizeDeploy: %v", err)
		}

		results2 := []appDeployResult{{App: "web", Identity: deployedAs("b2"), Record: edge.DeploymentRecord{App: "web", Identity: "b2"}}}
		if _, err := finalizeDeploy(ctx, Config{}, fake, specs, state, "promo2", "", "", 200, results2); err != nil {
			t.Fatalf("second finalizeDeploy: %v", err)
		}

		if len(fake.promotions) != 2 {
			t.Fatalf("promotions = %d, want 2 (both retained)", len(fake.promotions))
		}
		if fake.promotions[0].PromotionID != "promo1" || fake.promotions[1].PromotionID != "promo2" {
			t.Errorf("promotions = %+v, want promo1 then promo2", fake.promotions)
		}
	})
}

func TestFinalizeDeploy(t *testing.T) {
	t.Run("production promotes the reserved default pointer", func(t *testing.T) {
		ctx := context.Background()
		results := []appDeployResult{
			{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
		}

		prod := &recordingRootStack{}
		if _, err := finalizeDeploy(ctx, Config{}, prod, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "", 100, results); err != nil {
			t.Fatalf("finalizeDeploy(production): %v", err)
		}
		if len(prod.promotePointers) != 1 || prod.promotePointers[0] != "" {
			t.Errorf("production promote pointer = %v, want the reserved default (empty)", prod.promotePointers)
		}
	})

	t.Run("a preview promotes the given pointer", func(t *testing.T) {
		ctx := context.Background()
		results := []appDeployResult{
			{App: "web", Identity: deployedAs("b1"), Record: edge.DeploymentRecord{App: "web", Identity: "b1"}},
		}

		preview := &recordingRootStack{}
		if _, err := finalizeDeploy(ctx, Config{}, preview, []edge.RootStackSpec{{Version: "v1"}}, nil, "promo1", "", "pr-42", 100, results); err != nil {
			t.Fatalf("finalizeDeploy(preview): %v", err)
		}
		if len(preview.promotePointers) != 1 || preview.promotePointers[0] != "pr-42" {
			t.Errorf("preview promote pointer = %v, want [pr-42]", preview.promotePointers)
		}
	})

	t.Run("a rotation of one build is a new deployment and promotion", func(t *testing.T) {
		fake := &recordingRootStack{}
		ctx := context.Background()
		specs := []edge.RootStackSpec{{Version: "v1"}}
		before, after := deployedAs("B1"), fingerprinted("B1", "fp2")

		result := func(id Identity) []appDeployResult {
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
		if got := []string{fake.staged[0].Identity, fake.staged[1].Identity}; got[0] != before.String() || got[1] != after.String() {
			t.Errorf("staged identities = %v, want [%s %s]", got, before, after)
		}
		if len(fake.promotions) != 2 {
			t.Fatalf("promotions = %d, want 2: a rotation is its own promotion", len(fake.promotions))
		}
		if got := fake.promotions[1].Builds["web"]; got != after.String() {
			t.Errorf("rotation promotion Builds[web] = %q, want %q", got, after)
		}
		if got := fake.promotions[0].Builds["web"]; got != before.String() {
			t.Errorf("prior promotion Builds[web] = %q, want %q — the prior Deployment stays intact", got, before)
		}
		if a, b := appStack(t, ProductionEnv, "web", before), appStack(t, ProductionEnv, "web", after); a == b {
			t.Errorf("both Deployments name stack %q; a rotation must provision its own", a)
		}
	})

	t.Run("two rotations of one build are three distinct deployments", func(t *testing.T) {
		cfg := Config{ArtifactRoot: edgeAppTree(t), Env: "prod", Edge: testLoaderEdge()}
		manifest := nextManifest()
		app := &deploymentsv1.ManifestApp{Name: "web", Framework: frameworkNext}
		fingerprints := []string{"", "fp2", "fp3"}
		ids := make([]Identity, len(fingerprints))
		prefixes := map[string]bool{}

		fake := &recordingRootStack{}
		ctx := context.Background()
		specs := []edge.RootStackSpec{{Version: "v1"}}
		var state edge.RootStackState
		for i, fingerprint := range fingerprints {
			builds := releaseBuilds(t, cfg, manifest, fingerprint)
			id := builds.identities["web"]
			ids[i] = id
			record, err := buildDeploymentRecord(cfg, manifest, app, id, nil, builds)
			if err != nil {
				t.Fatalf("buildDeploymentRecord: %v", err)
			}
			next, err := finalizeDeploy(ctx, originStore(cfg), fake, specs, state, fmt.Sprintf("promo%d", i+1), "", "", int64(100*(i+1)), []appDeployResult{{App: "web", Identity: id, Record: record}})
			if err != nil {
				t.Fatalf("finalizeDeploy %d: %v", i+1, err)
			}
			state = next
			prefixes[record.AssetPrefix] = true
		}

		if len(fake.staged) != 3 || len(fake.promotions) != 3 {
			t.Fatalf("staged = %d records over %d promotions, want 3 and 3", len(fake.staged), len(fake.promotions))
		}
		names := map[naming.StackName]bool{}
		for i, id := range ids {
			want := id.String()
			if got := fake.staged[i].Identity; got != want {
				t.Errorf("staged[%d].Identity = %q, want %q", i, got, want)
			}
			if got := fake.promotions[i].Builds["web"]; got != want {
				t.Errorf("promotions[%d].Builds[web] = %q, want %q", i, got, want)
			}
			names[appStack(t, ProductionEnv, "web", ids[i])] = true
		}
		if len(names) != 3 {
			t.Errorf("app-deploy stack names = %v, want three distinct ones", names)
		}

		if len(prefixes) != 3 {
			t.Errorf("asset prefixes = %v, want one per release", prefixes)
		}
		for i, record := range fake.staged {
			root := "prod/proj/web/" + releaseOf(ids[i]).String() + "/"
			for name, key := range map[string]string{
				"AssetPrefix":           record.AssetPrefix,
				"IsrPrefix":             record.IsrPrefix,
				"EdgeWorkers.BundleKey": record.EdgeWorkers.BundleKey,
			} {
				if !strings.HasPrefix(key, root) {
					t.Errorf("staged[%d].%s = %q, want it under this release's prefix %q", i, name, key, root)
				}
			}
		}
	})

	t.Run("the promotion carries rendered identities", func(t *testing.T) {
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
	})
}

func TestPromotePointer(t *testing.T) {
	t.Parallel()

	t.Run("empty for production", func(t *testing.T) {
		t.Parallel()
		if got := promotePointer(Config{Class: deploymentsv1.Environment_CLASS_PRODUCTION, Identity: "ignored"}); got != "" {
			t.Errorf("promotePointer(production) = %q, want empty", got)
		}
	})

	t.Run("the identity for preview", func(t *testing.T) {
		t.Parallel()
		if got := promotePointer(Config{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-42"}); got != "pr-42" {
			t.Errorf("promotePointer(preview) = %q, want pr-42", got)
		}
	})
}

func TestPlanEnvironment(t *testing.T) {
	t.Parallel()

	t.Run("threads class lifecycle identity", func(t *testing.T) {
		t.Parallel()
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
	})
}

func TestBootstrapCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		class deploymentsv1.Environment_Class
		want  string
	}{
		{"production", deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap"},
		{"preview", deploymentsv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bootstrapCommand(Config{Class: tc.class}); got != tc.want {
				t.Errorf("bootstrapCommand(%s) = %q", tc.name, got)
			}
		})
	}
}

func TestRealizeRequiresADeploymentIDPerApp(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		apps  []*deploymentsv1.ManifestApp
		wants string
	}{
		{
			name:  "an app carrying none",
			apps:  []*deploymentsv1.ManifestApp{{Name: "web"}},
			wants: "web",
		},
		{
			name:  "one app of two carrying none",
			apps:  []*deploymentsv1.ManifestApp{{Name: "web", DeploymentId: testDeploymentID}, {Name: "admin"}},
			wants: "admin",
		},
		{
			name:  "an app carrying something no mint produces",
			apps:  []*deploymentsv1.ManifestApp{{Name: "web", DeploymentId: "build-TfctsWXpff2fKS"}},
			wants: "web",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{Edge: &recordingRootStack{}, StoreEndpoint: "https://store.example.com"}
			manifest := &deploymentsv1.Manifest{Slug: "shop", Apps: tc.apps}
			_, err := realize(context.Background(), cfg, manifest, nil, nil)
			if err == nil {
				t.Fatal("realize err = nil, want the app without a deployment id refused")
			}
			if !strings.Contains(err.Error(), "deployment id") || !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("realize err = %v, want it to name %q and its missing deployment id", err, tc.wants)
			}
		})
	}
}

func TestRealizeRefusesAStaleStore(t *testing.T) {
	t.Parallel()

	stale := edge.StoreSchemaVersion - 1
	cfg := Config{
		Edge:          &recordingRootStack{storeSchemaVersion: &stale},
		StoreEndpoint: fakeStoreEndpoint,
		Slug:          "shop",
	}
	_, err := realize(context.Background(), cfg, &deploymentsv1.Manifest{Slug: "shop"}, nil, nil)
	if err == nil {
		t.Fatal("realize err = nil, want the superseded store refused")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("realize err = %v, want it to point at `ocel bootstrap`", err)
	}
}

func TestRealizeRefusesAStoreThatCannotReportItsSchema(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Edge:          &recordingRootStack{storeSchemaVersionErr: edge.ErrStoreSchemaUnreadable},
		StoreEndpoint: fakeStoreEndpoint,
		Slug:          "shop",
	}
	_, err := realize(context.Background(), cfg, &deploymentsv1.Manifest{Slug: "shop"}, nil, nil)
	if err == nil {
		t.Fatal("realize err = nil, want the unreadable store refused")
	}
	if !strings.Contains(err.Error(), "predates") || !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("realize err = %v, want it to say the store predates the check and point at `ocel bootstrap`", err)
	}
	if strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("realize err = %v, want the 401 not surfaced as an authorization failure", err)
	}
}

func TestValidateTag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		{"the untagged default", "", false},
		{"a well formed tag", "v1.2.3", false},
		{"rejects a slash host side", "feature/x", true},
		{"rejects a space host side", "has space", true},
		{"rejects a non-ASCII character host side", "über", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateTag(tc.tag)
			if tc.wantErr && err == nil {
				t.Errorf("validateTag(%q) = nil, want an error", tc.tag)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateTag(%q) = %v, want nil", tc.tag, err)
			}
		})
	}
}

func TestCheckTagAvailable(t *testing.T) {
	taggedHistory := func() *recordingRootStack {
		return &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "promo-1", Tag: "v1.2.3"}, Active: true},
			},
		}
	}

	cases := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		{"rejects a tag already in use", "v1.2.3", true},
		{"allows a fresh tag", "v2.0.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := taggedHistory()
			ctx := context.Background()
			state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1"}, nil)
			if err != nil {
				t.Fatalf("ReconcileRootStack: %v", err)
			}

			err = checkTagAvailable(ctx, fake, state, tc.tag)
			if tc.wantErr && err == nil {
				t.Fatal("expected a duplicate tag to be rejected")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("checkTagAvailable rejected a fresh tag: %v", err)
			}
		})
	}

	t.Run("no op for an untagged deploy", func(t *testing.T) {
		fake := taggedHistory()
		if err := checkTagAvailable(context.Background(), fake, edge.RootStackState{edge.RootStackKeyEndpoint: "http://store"}, ""); err != nil {
			t.Errorf("untagged deploy should never fail the tag check: %v", err)
		}
	})

	t.Run("no op for a first deploy", func(t *testing.T) {
		fake := taggedHistory()
		if err := checkTagAvailable(context.Background(), fake, nil, "v1.2.3"); err != nil {
			t.Errorf("first deploy (no store) should never fail the tag check: %v", err)
		}
	})
}

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

func TestReconcileRootStack(t *testing.T) {
	t.Run("threads state across multiple specs", func(t *testing.T) {
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
	})

	t.Run("no specs returns prior unchanged", func(t *testing.T) {
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
	})
}

func TestResolvedIdentities(t *testing.T) {
	t.Parallel()

	nextApp := func(t *testing.T) (Config, *deploymentsv1.Manifest) {
		t.Helper()
		return Config{ArtifactRoot: writeTree(t, map[string]string{
				"apps/web/routing-manifest.json": `{"buildId":"WEB1"}`,
			})}, &deploymentsv1.Manifest{
				Slug: "proj",
				Apps: []*deploymentsv1.ManifestApp{{Name: "web", Framework: frameworkNext}},
			}
	}

	cases := []struct {
		name    string
		bundles map[string]appBundle
		want    Identity
	}{
		{"nothing baked still fingerprints the environment", nil, deployedAs(testDeploymentID)},
		{"baked values fingerprint the identity", map[string]appBundle{"web": {Fingerprint: "abc123"}}, fingerprinted(testDeploymentID, "abc123")},
		{"a live-only bundle bakes nothing", map[string]appBundle{"web": {Live: []byte("{}")}}, deployedAs(testDeploymentID)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, manifest := nextApp(t)

			builds, err := resolveAppBuilds(deployedConfig(cfg), deployedManifest(manifest), tc.bundles)
			if err != nil {
				t.Fatalf("resolveAppBuilds: %v", err)
			}
			if got := builds.identities["web"]; got != tc.want {
				t.Errorf("identities[web] = %+v, want %+v", got, tc.want)
			}
			if got, want := builds.identities["web"].String(), testDeploymentID+identitySeparator+tc.want.Fingerprint(); got != want {
				t.Errorf("rendered identity = %q, want %q", got, want)
			}
		})
	}

	t.Run("one build in two environments is two identities", func(t *testing.T) {
		t.Parallel()
		cfg, manifest := nextApp(t)

		prod, err := resolveAppBuilds(deployedConfig(cfg), deployedManifest(manifest), nil)
		if err != nil {
			t.Fatalf("resolveAppBuilds(production): %v", err)
		}
		cfg.Env = "pr-7"
		preview, err := resolveAppBuilds(deployedConfig(cfg), deployedManifest(manifest), nil)
		if err != nil {
			t.Fatalf("resolveAppBuilds(preview): %v", err)
		}

		if prod.identities["web"] == preview.identities["web"] {
			t.Fatalf("both environments claim the identity %s", prod.identities["web"])
		}
		if prod.coords["web"].Release == preview.coords["web"].Release {
			t.Errorf("both environments claim the release %s; one deploy would overwrite the other's artifacts", prod.coords["web"].Release)
		}
	})

	t.Run("a framework with no buildID gets a minted one", func(t *testing.T) {
		t.Parallel()
		manifest := &deploymentsv1.Manifest{
			Slug: "proj",
			Apps: []*deploymentsv1.ManifestApp{{Name: "api", Framework: "express"}},
		}

		cfg := Config{ArtifactRoot: t.TempDir()}
		builds, err := resolveAppBuilds(deployedConfig(cfg), deployedManifest(manifest), nil)
		if err != nil {
			t.Fatalf("resolveAppBuilds: %v", err)
		}
		if builds.ids["api"] == "" {
			t.Error("no build id was minted for api")
		}
		if got := builds.identities["api"]; got.DeploymentID() != testDeploymentID {
			t.Errorf("identities[api] = %+v, want the deployment's own id", got)
		}
	})
}

func specsArtifactRoot(t *testing.T, manifest *deploymentsv1.Manifest) string {
	t.Helper()
	root := t.TempDir()
	for _, app := range manifestApps(manifest) {
		writeRoutingManifest(t, root, app.GetName(), `{"buildId":"b1"}`)
	}
	return root
}

func TestStackTags(t *testing.T) {
	t.Parallel()

	release := naming.NewRelease("B1", "abc123")

	t.Run("an app stack carries every fact constant across it", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION}
		stack := naming.AppStack("prod", "web", release)

		tags := stackTags(cfg, stack, "p7", "d1", "B1")

		want := map[string]string{
			"ocel:managed-by": managedBy(),
			"ocel:project":    "shop",
			"ocel:env":        "prod",
			"ocel:env-class":  "production",
			"ocel:app":        "web",
			"ocel:release":    release.String(),
			"ocel:build":      "B1",
			"ocel:deployment": "d1",
			"ocel:promotion":  "p7",
			"ocel:stack":      stack.String(),
		}
		if !reflect.DeepEqual(tags, want) {
			t.Errorf("stackTags = %v, want %v", tags, want)
		}
	})

	t.Run("together with the resource's own tags the set is complete", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, ExpiresAt: 1760000000}
		stack := naming.AppStack("pr-7", "web", release)

		keys := map[string]bool{}
		for key := range stackTags(cfg, stack, "p7", "d1", "B1") {
			keys[key] = true
		}
		for key := range resourceTags(naming.KindFunction, "/api/users", nil) {
			keys[key] = true
		}

		for _, key := range []string{
			"ocel:managed-by", "ocel:project", "ocel:env", "ocel:env-class", "ocel:app",
			"ocel:release", "ocel:build", "ocel:deployment", "ocel:promotion", "ocel:component", "ocel:route",
			"ocel:stack", "ocel:expires-at",
		} {
			if !keys[key] {
				t.Errorf("no resource carries %s", key)
			}
		}
	})

	t.Run("a preview is classed as such and stamped with its expiry", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PREVIEW, ExpiresAt: 1760000000}

		tags := stackTags(cfg, naming.AppStack("pr-7", "web", release), "p7", "d1", "B1")

		if got, want := tags["ocel:env-class"], "preview"; got != want {
			t.Errorf("ocel:env-class = %q, want %q", got, want)
		}
		if got, want := tags["ocel:expires-at"], "1760000000"; got != want {
			t.Errorf("ocel:expires-at = %q, want %q", got, want)
		}
	})

	t.Run("the infra stack names itself and claims nothing that changes between deploys", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Class: deploymentsv1.Environment_CLASS_PRODUCTION}

		tags := infraStackTags(cfg, naming.InfraStack("prod"))

		if got, want := tags["ocel:stack"], "prod--infra"; got != want {
			t.Errorf("ocel:stack = %q, want %q", got, want)
		}
		for _, key := range []string{"ocel:release", "ocel:build", "ocel:promotion", "ocel:route", "ocel:component"} {
			if _, ok := tags[key]; ok {
				t.Errorf("infra stack carries %s = %q, want it absent", key, tags[key])
			}
		}
	})

	t.Run("managed-by names the tool and a version AWS accepts in a tag", func(t *testing.T) {
		t.Parallel()

		got := managedBy()
		if !strings.HasPrefix(got, "ocel-cli/") {
			t.Fatalf("managedBy = %q, want an ocel-cli/<version>", got)
		}
		for _, r := range got {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case strings.ContainsRune("+-=._:/@ ", r):
			default:
				t.Errorf("managedBy = %q, has %q which AWS rejects in a tag value", got, r)
			}
		}
	})
}

func TestDefaultTagsReachTheWholeProgram(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(map[string]map[string]string{"tags": {
		"ocel:project":    "shop",
		"ocel:expires-at": "1760000000",
	}})
	if err != nil {
		t.Fatal(err)
	}

	settings := &workspace.ProjectStack{Config: config.Map{
		config.MustMakeKey("aws", "defaultTags"): config.NewObjectValue(string(encoded)),
	}}
	path := filepath.Join(t.TempDir(), "Pulumi.prod--web--r3f8a1c9.yaml")
	if err := settings.Save(path); err != nil {
		t.Fatalf("save stack settings: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws:defaultTags", "ocel:project: shop", `ocel:expires-at: "1760000000"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("stack settings\n%s\nmissing %q", raw, want)
		}
	}
}

func TestEmitEngineTraceAttachesResourceIdentityOnlyToStandouts(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	start := time.Unix(6000, 0)
	trace := EngineTrace{
		ResourceCount: 2,
		Start:         start,
		End:           start.Add(5 * time.Second),
		Failed:        true,
		Standouts: []ResourceStandout{
			{
				Op:     apitype.OpCreate,
				Type:   "aws:s3/bucket:Bucket",
				Name:   "my-bucket",
				Start:  start,
				End:    start.Add(5 * time.Second),
				Failed: true,
			},
		},
	}

	emitEngineTrace(ft, parent, trace, nil)

	if len(ft.spans) != 2 {
		t.Fatalf("got %d spans, want 2 (batch + standout)", len(ft.spans))
	}

	batch := ft.spans[0]
	if batch.name != engineBatchSpanName {
		t.Fatalf("spans[0].name = %q, want the batch span name", batch.name)
	}
	for _, a := range batch.attrs {
		if a.Key == deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE || a.Key == deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME {
			t.Errorf("batch span carries resource identity attr %+v; it covers many resources", a)
		}
	}

	standout := ft.spans[1]
	var sawType, sawName bool
	for _, a := range standout.attrs {
		switch a.Key {
		case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE:
			sawType = true
			if a.Value != "aws:s3/bucket:Bucket" {
				t.Errorf("RESOURCE_TYPE = %q, want the type token", a.Value)
			}
		case deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME:
			sawName = true
			if a.Value != "my-bucket" {
				t.Errorf("RESOURCE_NAME = %q, want the logical name", a.Value)
			}
			if strings.Contains(a.Value, "urn:pulumi") {
				t.Fatal("RESOURCE_NAME carried the raw URN")
			}
		}
	}
	if !sawType {
		t.Error("standout span is missing ATTRIBUTE_KEY_RESOURCE_TYPE")
	}
	if !sawName {
		t.Error("standout span is missing ATTRIBUTE_KEY_RESOURCE_NAME")
	}
}

func TestEmitEngineTraceOmitsResourceIdentityWhenTheURNDidNotParse(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	start := time.Unix(7000, 0)
	trace := EngineTrace{
		ResourceCount: 1,
		Start:         start,
		End:           start.Add(time.Second),
		Failed:        true,
		Standouts: []ResourceStandout{
			{Op: apitype.OpCreate, Type: "", Name: "", Start: start, End: start.Add(time.Second), Failed: true},
		},
	}

	emitEngineTrace(ft, parent, trace, nil)

	if len(ft.spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(ft.spans))
	}
	for _, a := range ft.spans[1].attrs {
		if a.Key == deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE || a.Key == deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME {
			t.Errorf("standout span carries resource identity attr %+v despite an unparseable URN", a)
		}
	}
}

func TestEmitEngineTraceStillEmitsOnAnUpErrorWithNoResourceOperations(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	start := time.Unix(8000, 0)
	trace := EngineTrace{Start: start, End: start.Add(time.Second)}

	emitEngineTrace(ft, parent, trace, errors.New("plugin failed to start"))

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1: a Pulumi run that failed before touching a resource must still leave a span", len(ft.spans))
	}
	if ft.spans[0].err == nil {
		t.Error("batch span not recorded as failed")
	}
}

func TestEmitEngineTraceStaysSilentOnAQuietSuccessfulRun(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	trace := EngineTrace{}

	emitEngineTrace(ft, parent, trace, nil)

	if len(ft.spans) != 0 {
		t.Fatalf("got %d spans, want 0: nothing happened and nothing failed", len(ft.spans))
	}
}

func TestAwaitEngineTraceReturnsWithinGraceOnAChannelThatIsNeverSentTo(t *testing.T) {
	t.Parallel()

	result := make(chan EngineTrace)
	done := make(chan EngineTrace, 1)
	go func() { done <- awaitEngineTrace(result, 20*time.Millisecond) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitEngineTrace did not return within its grace period")
	}
}

func TestStartEngineTraceDrainDoesNotBlockWhenEngineEventsIsNeverClosed(t *testing.T) {
	t.Parallel()

	engineEvents := make(chan events.EngineEvent, 4)
	engineEvents <- events.EngineEvent{}

	result := startEngineTraceDrain(engineEvents, 0)

	trace := awaitEngineTrace(result, 50*time.Millisecond)
	if !reflect.DeepEqual(trace, EngineTrace{}) {
		t.Errorf("got %+v, want a zero-value trace: the channel is unclosed so the builder goroutine never sent a result", trace)
	}
}
