package cloudflare

import (
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func programmed(slug string, tier environment.Tier) EntryProgram {
	return EntryProgram{
		Tier:      tier,
		Entry:     edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Namespace: defaultNamespace,
		Slug:      slug,
		Env:       "prod",
		Origin: OriginBindings{
			Variables: map[string]string{"OCEL_ORIGIN_ONLY": "v"},
			Secrets:   map[string]string{"OCEL_ORIGIN_KEY": "s"},
		},
		Values:                   map[string]string{"cacheBucket": "ocel-edge-cache-preview"},
		StoreScriptName:          "ocel-deployments-store-preview",
		StoreEndpoint:            "https://store.example",
		StoreBootstrapCredential: "store-cred",
		ISRWriterScriptName:      "ocel-isr-writer-preview",
	}
}

const previewKey edge.PreviewKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func TestTheSharedPreviewEntryProgramBindsTheStoreWorkerAndTheBaseDomain(t *testing.T) {
	entry := programmed("", environment.TierPreview)
	entry.PreviewBaseDomain = "preview.acme.com"
	entry.PreviewKey = previewKey

	built, err := entry.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	worker := built.Spec.Worker
	if string(worker.Main.Content) != "export default {}" {
		t.Errorf("Main = %q, want the generic bundle for the edge", worker.Main.Content)
	}
	if worker.Services[genericStoreBinding] != entry.StoreScriptName {
		t.Errorf("Services = %v, want the preview store bound", worker.Services)
	}
	for name, want := range map[string]string{
		envPreview:           "1",
		envPreviewGlobal:     "1",
		envPreviewBaseDomain: "preview.acme.com",
		"OCEL_ORIGIN_ONLY":   "v",
	} {
		if worker.Variables[name] != want {
			t.Errorf("Variables[%s] = %q, want %q", name, worker.Variables[name], want)
		}
	}
	if worker.Secrets["OCEL_ORIGIN_KEY"] != "s" {
		t.Errorf("Secrets[OCEL_ORIGIN_KEY] missing, want the origin's secret delivered as a secret")
	}
	if worker.Secrets[edge.PreviewKeyVar] != string(previewKey) {
		t.Errorf("Secrets[%s] missing, want the key that signs preview hostnames delivered as a secret", edge.PreviewKeyVar)
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

func TestEntryProgramRefusesAPreviewEntryWithNoStoreWorker(t *testing.T) {
	entry := programmed("", environment.TierPreview)
	entry.PreviewBaseDomain = "preview.acme.com"
	entry.PreviewKey = previewKey
	entry.StoreScriptName = ""

	_, err := entry.Build()
	if err == nil {
		t.Fatal("Build succeeded, want a preview entry with no deployments-store worker refused")
	}
	if !strings.Contains(err.Error(), provider.BootstrapCommand(environment.TierPreview)) {
		t.Errorf("error = %q, want it to name the bootstrap that provisions the store", err)
	}
}

func TestEntryProgramRefusesAWorkerThatVerifiesPreviewLabelsWithNoKey(t *testing.T) {
	for name, program := range map[string]EntryProgram{
		"the shared preview entry":            programmed("", environment.TierPreview),
		"a project on its own preview domain": programmed("proj", environment.TierPreview),
	} {
		t.Run(name, func(t *testing.T) {
			program.PreviewBaseDomain = "preview.acme.com"
			if _, err := program.Build(); err == nil {
				t.Fatal("Build succeeded, want a worker that can verify no preview hostname refused")
			}
		})
	}
}

func TestAPreviewProjectEntryProgramNamesItsWorkerAndReadsTheStoreOverItsEndpoint(t *testing.T) {
	project := programmed("proj", environment.TierPreview)
	project.PreviewBaseDomain = "preview.acme.com"
	project.PreviewKey = previewKey

	built, err := project.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Spec.Name != previewWorkerName(defaultNamespace, "proj") {
		t.Errorf("Name = %q, want %q", built.Spec.Name, previewWorkerName(defaultNamespace, "proj"))
	}
	if built.Spec.PruneWorkerStem != previewWorkerStem(defaultNamespace, "proj") {
		t.Errorf("PruneWorkerStem = %q, want %q", built.Spec.PruneWorkerStem, previewWorkerStem(defaultNamespace, "proj"))
	}
	if built.Spec.RequiredRecord != "" {
		t.Errorf("RequiredRecord = %q, want empty: ocel plants the records for the domains it serves", built.Spec.RequiredRecord)
	}
	for name, want := range map[string]string{
		envPreview:           "1",
		envPreviewBaseDomain: "preview.acme.com",
	} {
		if built.Spec.Worker.Variables[name] != want {
			t.Errorf("Variables[%s] = %q, want %q", name, built.Spec.Worker.Variables[name], want)
		}
	}
	for name, want := range map[string]string{
		edge.PreviewKeyVar: string(previewKey),
		"OCEL_ORIGIN_KEY":  "s",
	} {
		if built.Spec.Worker.Secrets[name] != want {
			t.Errorf("Secrets[%s] = %q, want %q", name, built.Spec.Worker.Secrets[name], want)
		}
	}
	if _, bound := built.Spec.Worker.Services[genericStoreBinding]; bound {
		t.Errorf("Services = %v, want no store binding: a project worker reads the store over its endpoint", built.Spec.Worker.Services)
	}
	if built.Spec.StoreEndpoint != project.StoreEndpoint || built.Spec.BootstrapCredential != project.StoreBootstrapCredential {
		t.Errorf("store = %q/%q, want %q/%q",
			built.Spec.StoreEndpoint, built.Spec.BootstrapCredential, project.StoreEndpoint, project.StoreBootstrapCredential)
	}
}

func TestEntryProgramForAPreviewProjectOnTheSharedWildcard(t *testing.T) {
	project := programmed("proj", environment.TierPreview)

	built, err := project.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, set := built.Spec.Worker.Variables[envPreviewBaseDomain]; set {
		t.Errorf("Variables has %s = %q, want none: the shared preview entry has the base domain, not the project's worker",
			envPreviewBaseDomain, built.Spec.Worker.Variables[envPreviewBaseDomain])
	}
	if built.Spec.Worker.Variables[envPreview] != "1" {
		t.Errorf("Variables[%s] = %q, want 1", envPreview, built.Spec.Worker.Variables[envPreview])
	}
	if built.Spec.Name != previewWorkerName(defaultNamespace, "proj") || built.Spec.PruneWorkerStem != previewWorkerStem(defaultNamespace, "proj") {
		t.Errorf("name = %q, stem = %q, want the project's preview worker named so the edge can sweep it",
			built.Spec.Name, built.Spec.PruneWorkerStem)
	}
}

func TestAProductionProjectEntryProgramSweepsItsOwnScriptAndCarriesNoPreviewConfig(t *testing.T) {
	built, err := programmed("proj", environment.TierProduction).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Spec.Name != rootWorkerName(defaultNamespace, "proj", "prod") {
		t.Errorf("Name = %q, want %q", built.Spec.Name, rootWorkerName(defaultNamespace, "proj", "prod"))
	}
	if built.Spec.PruneWorkerStem != "" {
		t.Errorf("PruneWorkerStem = %q, want empty: a production spec sweeps its own script alone", built.Spec.PruneWorkerStem)
	}
	for _, unwanted := range []string{envPreview, envPreviewGlobal, envPreviewBaseDomain} {
		if _, set := built.Spec.Worker.Variables[unwanted]; set {
			t.Errorf("Variables has %s, which belongs to a preview", unwanted)
		}
	}
	if _, set := built.Spec.Worker.Secrets[edge.PreviewKeyVar]; set {
		t.Errorf("Secrets has %s, which belongs to a preview", edge.PreviewKeyVar)
	}
}

func TestEntryProgramRunsTheEntryModuleTheEdgeNames(t *testing.T) {
	program := programmed("shop", environment.TierProduction)

	built, err := program.Build()
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}
	if main := built.Spec.Worker.Main; main.Name != program.Entry.Name || string(main.Content) != string(program.Entry.Content) {
		t.Errorf("Worker.Main = %q %q, want the edge's entry %q %q", main.Name, main.Content, program.Entry.Name, program.Entry.Content)
	}
}

func TestEntryProgramRefusesAnEdgeThatNamesNoEntryModule(t *testing.T) {
	program := programmed("shop", environment.TierProduction)
	program.Entry = edge.WorkerModule{}

	if _, err := program.Build(); err == nil || !strings.Contains(err.Error(), string(Kind)) {
		t.Fatalf("Build() = %v, want a refusal naming the edge", err)
	}
}

func TestEntryProgramPassesTheOriginsBindingsThroughUnchanged(t *testing.T) {
	program := programmed("shop", environment.TierProduction)
	program.Origin = OriginBindings{
		Variables: map[string]string{"A": "1", "B": "2"},
		Secrets:   map[string]string{"S": "x", "T": "y"},
	}

	built, err := program.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !maps.Equal(built.Spec.Worker.Variables, program.Origin.Variables) {
		t.Errorf("Variables = %v, want %v", built.Spec.Worker.Variables, program.Origin.Variables)
	}
	if !maps.Equal(built.Spec.Worker.Secrets, program.Origin.Secrets) {
		t.Errorf("Secrets = %v, want %v", built.Spec.Worker.Secrets, program.Origin.Secrets)
	}
}

func TestEntryProgramLeavesTheCallersBindingsUntouched(t *testing.T) {
	program := programmed("", environment.TierPreview)
	program.PreviewBaseDomain = "preview.acme.com"
	program.PreviewKey = previewKey
	variables := maps.Clone(program.Origin.Variables)
	secrets := maps.Clone(program.Origin.Secrets)

	if _, err := program.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !maps.Equal(program.Origin.Variables, variables) {
		t.Errorf("Origin.Variables = %v, want %v", program.Origin.Variables, variables)
	}
	if !maps.Equal(program.Origin.Secrets, secrets) {
		t.Errorf("Origin.Secrets = %v, want %v", program.Origin.Secrets, secrets)
	}
}

func TestEntryProgramWithNoOriginSecretsCarriesNoSecrets(t *testing.T) {
	program := programmed("shop", environment.TierProduction)
	program.Origin = OriginBindings{}

	built, err := program.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Spec.Worker.Secrets != nil {
		t.Errorf("Secrets = %v, want nil", built.Spec.Worker.Secrets)
	}
	if built.Spec.Worker.Variables == nil || len(built.Spec.Worker.Variables) != 0 {
		t.Errorf("Variables = %#v, want non-nil and empty", built.Spec.Worker.Variables)
	}
}

func TestAnEntryWorkerGivenAnOriginClientCertificateBindsItForMutualTLS(t *testing.T) {
	program := programmed("shop", environment.TierProduction)
	program.Origin = OriginBindings{ClientCertificate: "c1"}

	built, err := program.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := map[string]string{edge.OriginClientCertificateBinding: "c1"}
	if !maps.Equal(built.Spec.Worker.ClientCertificates, want) {
		t.Errorf("ClientCertificates = %v, want %v", built.Spec.Worker.ClientCertificates, want)
	}
}

func TestAnEntryWorkerRefusesAnOriginThatHandsBothAWSKeysAndAClientCertificate(t *testing.T) {
	program := programmed("shop", environment.TierProduction)
	program.Origin = OriginBindings{
		ClientCertificate: "c1",
		Variables:         map[string]string{edge.EdgeAccessKeyIDVar: "AKIA"},
		Secrets:           map[string]string{edge.EdgeSecretKeyVar: "s"},
	}

	if _, err := program.Build(); err == nil || !strings.Contains(err.Error(), "client certificate") {
		t.Fatalf("Build() = %v, want a refusal naming the client certificate", err)
	}
}

func TestAnEntryWorkerWithNoOriginClientCertificateCarriesNone(t *testing.T) {
	program := programmed("shop", environment.TierProduction)
	program.Origin = OriginBindings{}

	built, err := program.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.Spec.Worker.ClientCertificates != nil {
		t.Errorf("ClientCertificates = %v, want nil", built.Spec.Worker.ClientCertificates)
	}
}
