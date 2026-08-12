package deploy

import (
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPreviewLabelProblem(t *testing.T) {
	t.Parallel()

	t.Run("a label within the cap passes", func(t *testing.T) {
		t.Parallel()
		if err := PreviewLabelProblem("acme", []string{"acme--main--admin.preview.acme.com"}); err != nil {
			t.Fatalf("PreviewLabelProblem: %v", err)
		}
	})

	t.Run("a wildcard carries no label to check", func(t *testing.T) {
		t.Parallel()
		if err := PreviewLabelProblem("acme", []string{"*.preview.acme.com"}); err != nil {
			t.Fatalf("PreviewLabelProblem: %v", err)
		}
	})

	t.Run("an over-long global label names all three components", func(t *testing.T) {
		t.Parallel()
		pointer := strings.Repeat("b", 60)
		err := PreviewLabelProblem("acme", []string{"acme--" + pointer + "--admin.preview.acme.com"})
		if err == nil {
			t.Fatal("expected a 73-character label to be refused")
		}
		for _, want := range []string{"acme", pointer, "admin", "63", "73", "10"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q, got %q", want, err)
			}
		}
	})

	t.Run("an over-long project label names the preview", func(t *testing.T) {
		t.Parallel()
		pointer := strings.Repeat("p", previewLabelMaxLen+1)
		err := PreviewLabelProblem("acme", []string{pointer + ".preview.acme.com"})
		if err == nil {
			t.Fatalf("expected a pointer of %d characters to be refused", len(pointer))
		}
		if !strings.Contains(err.Error(), pointer) || !strings.Contains(err.Error(), "64") {
			t.Errorf("error must name the preview and the label length, got %q", err)
		}
	})
}

func TestSharedPreviewEntrySpec(t *testing.T) {
	setWorkerBundle(t)

	cfg := Config{
		Edge:                &recordingEdge{},
		Region:              "eu-west-1",
		StateTable:          "ocel-state",
		AssetBucket:         "ocel-assets",
		ImageOptimizerURL:   "https://optimizer.example",
		RevalidateQueueURL:  "https://queue.example",
		EdgeAccessKeyID:     "AKIA",
		EdgeSecretKey:       "secret",
		StoreScriptName:     "ocel-deployments-store-preview",
		ISRWriterScriptName: "ocel-isr-writer-preview",
		EdgeValues:          map[string]string{"cacheBucket": "ocel-edge-cache-preview"},
	}

	spec, err := SharedPreviewEntrySpec(cfg, "preview.acme.com", nil)
	if err != nil {
		t.Fatalf("SharedPreviewEntrySpec: %v", err)
	}
	if spec.ScriptName != edge.SharedPreviewEntryScript {
		t.Errorf("ScriptName = %q, want %q", spec.ScriptName, edge.SharedPreviewEntryScript)
	}
	if spec.BaseDomain != "preview.acme.com" {
		t.Errorf("BaseDomain = %q", spec.BaseDomain)
	}
	if spec.GrammarMin != edge.PreviewGrammarMin || spec.GrammarMax != edge.PreviewGrammarMax {
		t.Errorf("grammar = [%d,%d], want [%d,%d]", spec.GrammarMin, spec.GrammarMax, edge.PreviewGrammarMin, edge.PreviewGrammarMax)
	}
	if spec.Generic.Services[storeServiceBinding] != cfg.StoreScriptName {
		t.Errorf("Services = %v, want the preview store bound", spec.Generic.Services)
	}
	for name, want := range map[string]string{
		envPreview:                 "1",
		envPreviewGlobal:           "1",
		envPreviewBaseDomain:       "preview.acme.com",
		edge.AWSRegionVar:          "eu-west-1",
		edge.StateTableVar:         "ocel-state",
		edge.AssetBucketVar:        "ocel-assets",
		edge.ImageOptimizerURLVar:  "https://optimizer.example",
		edge.RevalidateQueueURLVar: "https://queue.example",
		edge.EdgeAccessKeyIDVar:    "AKIA",
	} {
		if spec.Generic.Vars[name] != want {
			t.Errorf("Vars[%s] = %q, want %q", name, spec.Generic.Vars[name], want)
		}
	}
	if spec.Generic.Secrets[edge.EdgeSecretKeyVar] != "secret" {
		t.Errorf("Secrets[%s] missing", edge.EdgeSecretKeyVar)
	}
	for _, unwanted := range []string{envPreviewApps, "OCEL_SLUG"} {
		if _, ok := spec.Generic.Vars[unwanted]; ok {
			t.Errorf("Vars carries %s, which is per-project", unwanted)
		}
	}

	if spec.Values["cacheBucket"] != "ocel-edge-cache-preview" {
		t.Errorf("Values = %v, want the cache bucket the entry serves assets from", spec.Values)
	}
	if spec.ISRWriterScriptName != cfg.ISRWriterScriptName {
		t.Errorf("ISRWriterScriptName = %q, want %q", spec.ISRWriterScriptName, cfg.ISRWriterScriptName)
	}

	if _, err := SharedPreviewEntrySpec(cfg, "", nil); err == nil {
		t.Error("expected an empty domain to be refused")
	}
}

func TestMarkGlobalPreview(t *testing.T) {
	t.Parallel()

	manifest := func() *deploymentsv1.Manifest {
		return &deploymentsv1.Manifest{Slug: "proj"}
	}
	preview := Config{Slug: "proj", Class: deploymentsv1.Environment_CLASS_PREVIEW, GlobalPreviewDomain: "preview.acme.com"}
	state := func() edge.RootStackState {
		return edge.RootStackState{edge.RootStackKeySlug: "proj"}
	}

	t.Run("an ambient project records the domain serving it", func(t *testing.T) {
		t.Parallel()
		marked := MarkGlobalPreview(state(), preview, manifest())
		if !edge.ServedOnGlobalPreview(marked, "preview.acme.com") {
			t.Errorf("state = %v, want it served on preview.acme.com", marked)
		}
	})

	t.Run("a project on its own preview domain records nothing", func(t *testing.T) {
		t.Parallel()
		m := manifest()
		m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}

		marked := MarkGlobalPreview(state(), preview, m)
		if edge.ServedOnGlobalPreview(marked, "preview.acme.com") {
			t.Errorf("state = %v, want no global-preview mark", marked)
		}
	})

	t.Run("declaring a domain later clears the mark", func(t *testing.T) {
		t.Parallel()
		m := manifest()
		m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
		prior := state()
		prior[edge.RootStackKeyGlobalPreview] = "preview.acme.com"

		marked := MarkGlobalPreview(prior, preview, m)
		if _, ok := marked[edge.RootStackKeyGlobalPreview]; ok {
			t.Errorf("state = %v, want the stale mark gone", marked)
		}
	})

	t.Run("production is never marked", func(t *testing.T) {
		t.Parallel()
		cfg := preview
		cfg.Class = deploymentsv1.Environment_CLASS_PRODUCTION

		if marked := MarkGlobalPreview(state(), cfg, manifest()); edge.ServedOnGlobalPreview(marked, "preview.acme.com") {
			t.Errorf("state = %v, want no mark on a production deploy", marked)
		}
	})

	t.Run("no state is left alone", func(t *testing.T) {
		t.Parallel()
		if marked := MarkGlobalPreview(nil, preview, manifest()); marked != nil {
			t.Errorf("state = %v, want nil", marked)
		}
	})
}

func TestRootStackSpecsGlobalPreview(t *testing.T) {
	setWorkerBundle(t)
	setStoreWorkerBundle(t)

	manifest := func() *deploymentsv1.Manifest {
		return &deploymentsv1.Manifest{
			Slug:      "proj",
			Apps:      []*deploymentsv1.ManifestApp{{Name: "web", Framework: "next"}},
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", RouteId: "/"}},
		}
	}

	t.Run("an ambient project claims no hostname of its own", func(t *testing.T) {
		m := manifest()
		cfg := Config{
			Edge:                &recordingEdge{},
			Slug:                "proj",
			Class:               deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:            "pr-42",
			GlobalPreviewDomain: "preview.acme.com",
			ArtifactRoot:        specsArtifactRoot(t, m),
		}

		specs, err := rootStackSpecs(cfg, m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1: the shared entry serves this project, but its store instance is still its own", len(specs))
		}
		if len(specs[0].Domains) != 0 {
			t.Errorf("Domains = %v, want none", specs[0].Domains)
		}
		if specs[0].PruneWorkerStem != previewWorkerStem("proj") {
			t.Errorf("PruneWorkerStem = %q, want %q", specs[0].PruneWorkerStem, previewWorkerStem("proj"))
		}
	})

	t.Run("a declared preview domain beats the substrate's", func(t *testing.T) {
		m := manifest()
		m.Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
		cfg := Config{
			Edge:                &recordingEdge{},
			Slug:                "proj",
			Class:               deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:            "pr-42",
			GlobalPreviewDomain: "preview.acme.com",
			ArtifactRoot:        specsArtifactRoot(t, m),
		}

		specs, err := rootStackSpecs(cfg, m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
		if !slicesEqual(specs[0].Domains, []string{"*.preview.proj.com"}) {
			t.Errorf("Domains = %v, want the project's own wildcard", specs[0].Domains)
		}
	})

	t.Run("an app-level preview domain beats the substrate's", func(t *testing.T) {
		m := manifest()
		m.Apps[0].Domains = map[string]*deploymentsv1.DomainList{"preview": {Hostnames: []string{"*.preview.proj.com"}}}
		cfg := Config{
			Edge:                &recordingEdge{},
			Slug:                "proj",
			Class:               deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:            "pr-42",
			GlobalPreviewDomain: "preview.acme.com",
			ArtifactRoot:        specsArtifactRoot(t, m),
		}

		specs, err := rootStackSpecs(cfg, m, "v1", nil)
		if err != nil {
			t.Fatalf("rootStackSpecs: %v", err)
		}
		if len(specs) != 1 {
			t.Fatalf("specs = %d, want 1", len(specs))
		}
	})

	t.Run("no recorded domain keeps the refusal", func(t *testing.T) {
		m := manifest()
		cfg := Config{
			Edge:         &recordingEdge{},
			Slug:         "proj",
			Class:        deploymentsv1.Environment_CLASS_PREVIEW,
			Identity:     "pr-42",
			ArtifactRoot: specsArtifactRoot(t, m),
		}

		if _, err := rootStackSpecs(cfg, m, "v1", nil); err == nil {
			t.Fatal("expected a preview deploy with nowhere to serve to be refused")
		}
	})
}
