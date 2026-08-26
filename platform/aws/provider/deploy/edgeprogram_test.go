package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func setWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.KindBundleManifest{cloudflare.Kind: bundle})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.EnvWorkerBundles, string(raw))
}

func programmed(slug string, class providerkit.Class) EdgeProgram {
	return EdgeProgram{
		Class: class,
		Kind:  cloudflare.Kind,
		Slug:  slug,
		Env:   "prod",
		Worker: WorkerFacts{
			Region:             "eu-west-1",
			StateTable:         "ocel-state",
			AssetBucket:        "ocel-assets",
			ImageOptimizerURL:  "https://optimizer.example",
			RevalidateQueueURL: "https://queue.example",
			EdgeAccessKeyID:    "AKIA",
			EdgeSecretKey:      "secret",
		},
		Values:              map[string]string{"cacheBucket": "ocel-edge-cache-preview"},
		StoreScriptName:     "ocel-deployments-store-preview",
		StoreEndpoint:       "https://store.example",
		StoreBootstrapCred:  "store-cred",
		ISRWriterScriptName: "ocel-isr-writer-preview",
	}
}

func TestEdgeProgramForTheSharedPreviewEntry(t *testing.T) {
	setWorkerBundle(t)

	entry := programmed("", providerkit.ClassPreview)
	entry.PreviewBaseDomain = "preview.acme.com"

	built, err := entry.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	worker := built.Spec.Worker
	if string(worker.Main.Content) != "export default {}" {
		t.Errorf("Main = %q, want the generic bundle for the edge", worker.Main.Content)
	}
	if worker.Services[storeServiceBinding] != entry.StoreScriptName {
		t.Errorf("Services = %v, want the preview store bound", worker.Services)
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
		edge.OriginBodyEncodingVar: edge.OriginBodyEncodingBase64,
	} {
		if worker.Vars[name] != want {
			t.Errorf("Vars[%s] = %q, want %q", name, worker.Vars[name], want)
		}
	}
	if worker.Secrets[edge.EdgeSecretKeyVar] != "secret" {
		t.Errorf("Secrets[%s] missing, want the edge credential delivered as a secret", edge.EdgeSecretKeyVar)
	}
	if _, carried := worker.Vars[envPreviewApps]; carried {
		t.Errorf("Vars carries %s, which is per-project", envPreviewApps)
	}
	if built.Spec.Name != "" {
		t.Errorf("Name = %q, want empty: the edge names the shared entry itself", built.Spec.Name)
	}
	if built.Spec.ISRWriterScriptName != entry.ISRWriterScriptName {
		t.Errorf("ISRWriterScriptName = %q, want %q", built.Spec.ISRWriterScriptName, entry.ISRWriterScriptName)
	}
	if built.Values["cacheBucket"] != "ocel-edge-cache-preview" {
		t.Errorf("Values = %v, want the cache bucket the entry serves assets from", built.Values)
	}
}

func TestEdgeProgramRefusesAPreviewEntryWithNoStoreWorker(t *testing.T) {
	setWorkerBundle(t)

	entry := programmed("", providerkit.ClassPreview)
	entry.PreviewBaseDomain = "preview.acme.com"
	entry.StoreScriptName = ""

	_, err := entry.Build()
	if err == nil {
		t.Fatal("Build succeeded, want a preview entry with no deployments-store worker refused")
	}
	if !strings.Contains(err.Error(), providerkit.BootstrapCommand(providerkit.ClassPreview)) {
		t.Errorf("error = %q, want it to name the bootstrap that provisions the store", err)
	}
}

func TestEdgeProgramForAPreviewProject(t *testing.T) {
	setWorkerBundle(t)

	project := programmed("proj", providerkit.ClassPreview)
	project.PreviewBaseDomain = "preview.acme.com"
	project.Apps = []string{"web", "admin"}

	built, err := project.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Spec.Name != previewWorkerName("proj") {
		t.Errorf("Name = %q, want %q", built.Spec.Name, previewWorkerName("proj"))
	}
	if built.Spec.PruneWorkerStem != previewWorkerStem("proj") {
		t.Errorf("PruneWorkerStem = %q, want %q", built.Spec.PruneWorkerStem, previewWorkerStem("proj"))
	}
	if built.Spec.RequiredRecord != "" {
		t.Errorf("RequiredRecord = %q, want empty: ocel plants the records for the domains it serves", built.Spec.RequiredRecord)
	}
	for name, want := range map[string]string{
		envPreview:           "1",
		envPreviewApps:       "web,admin",
		envPreviewBaseDomain: "preview.acme.com",
	} {
		if built.Spec.Worker.Vars[name] != want {
			t.Errorf("Vars[%s] = %q, want %q", name, built.Spec.Worker.Vars[name], want)
		}
	}
	if _, bound := built.Spec.Worker.Services[storeServiceBinding]; bound {
		t.Errorf("Services = %v, want no store binding: a project worker reads the store over its endpoint", built.Spec.Worker.Services)
	}
	if built.Spec.StoreEndpoint != project.StoreEndpoint || built.Spec.BootstrapCred != project.StoreBootstrapCred {
		t.Errorf("store = %q/%q, want %q/%q",
			built.Spec.StoreEndpoint, built.Spec.BootstrapCred, project.StoreEndpoint, project.StoreBootstrapCred)
	}
}

func TestEdgeProgramForAPreviewProjectOnTheSharedWildcard(t *testing.T) {
	setWorkerBundle(t)

	project := programmed("proj", providerkit.ClassPreview)
	project.Apps = []string{"web", "admin"}

	built, err := project.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, carried := built.Spec.Worker.Vars[envPreviewBaseDomain]; carried {
		t.Errorf("Vars carries %s = %q, want none: the shared preview entry holds the base domain, not the project's worker",
			envPreviewBaseDomain, built.Spec.Worker.Vars[envPreviewBaseDomain])
	}
	for name, want := range map[string]string{envPreview: "1", envPreviewApps: "web,admin"} {
		if built.Spec.Worker.Vars[name] != want {
			t.Errorf("Vars[%s] = %q, want %q", name, built.Spec.Worker.Vars[name], want)
		}
	}
	if built.Spec.Name != previewWorkerName("proj") || built.Spec.PruneWorkerStem != previewWorkerStem("proj") {
		t.Errorf("name = %q, stem = %q, want the project's preview worker named so the edge can sweep it",
			built.Spec.Name, built.Spec.PruneWorkerStem)
	}
}

func TestEdgeProgramForAProductionProject(t *testing.T) {
	setWorkerBundle(t)

	built, err := programmed("proj", providerkit.ClassProduction).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Spec.Name != rootWorkerName("proj", "prod") {
		t.Errorf("Name = %q, want %q", built.Spec.Name, rootWorkerName("proj", "prod"))
	}
	if built.Spec.PruneWorkerStem != "" {
		t.Errorf("PruneWorkerStem = %q, want empty: a production spec sweeps its own script alone", built.Spec.PruneWorkerStem)
	}
	for _, unwanted := range []string{envPreview, envPreviewGlobal, envPreviewApps, envPreviewBaseDomain} {
		if _, carried := built.Spec.Worker.Vars[unwanted]; carried {
			t.Errorf("Vars carries %s, which belongs to a preview", unwanted)
		}
	}
}
