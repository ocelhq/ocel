package cloudflare

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const previewKey edge.PreviewKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func signedReach() edge.OriginReach {
	return edge.OriginReach{
		Reach:    edge.Reach{Kind: edge.ReachSigV4, Service: "lambda", Region: "eu-west-2", AccessKeyID: "AKIA", SecretAccessKey: "s"},
		Store:    &edge.OriginStore{URL: "https://assets.s3.eu-west-2.amazonaws.com", Region: "eu-west-2", HoldsEntries: true, AccessKeyID: "AKIA", SecretAccessKey: "s"},
		Throttle: edge.Throttle{Status: 429, Header: "x-amzn-errortype"},
	}
}

func certifiedReach() edge.OriginReach {
	return edge.OriginReach{
		Reach:    edge.Reach{Kind: edge.ReachClientCertificate, Certificate: "c1"},
		Throttle: edge.Throttle{Status: 429, BodyContains: "no available instance"},
	}
}

func programmed() *edge.Program {
	return &edge.Program{
		Origin: signedReach(),
		ReleasesStore: edge.ReleasesStore{
			Name:                "ocel-preview-releases-store",
			Endpoint:            "https://store.example",
			BootstrapCredential: "store-cred",
		},
	}
}

func previewProgrammed() *edge.Program {
	program := programmed()
	program.PreviewBaseDomain = "preview.acme.com"
	program.PreviewKey = previewKey
	return program
}

func buildEntry(t *testing.T, tier environment.Tier, slug string, program *edge.Program) entryProgram {
	t.Helper()
	built, err := newCloudflare(defaultNamespace, Options{}).buildEntryProgram(tier, slug, program)
	if err != nil {
		t.Fatalf("buildEntryProgram: %v", err)
	}
	return built
}

func everyEntry(t *testing.T) map[string]entryProgram {
	t.Helper()
	return map[string]entryProgram{
		"the shared preview entry": buildEntry(t, environment.TierPreview, "", previewProgrammed()),
		"a project's preview":      buildEntry(t, environment.TierPreview, "acme", programmed()),
		"a project's production":   buildEntry(t, environment.TierProduction, "acme", programmed()),
	}
}

func TestEveryEntryWorkerCachesItsServeEntrypointAndQueuesPurgesInADurableObject(t *testing.T) {
	for name, built := range everyEntry(t) {
		t.Run(name, func(t *testing.T) {
			if got := built.worker.CachedEntrypoints; !slices.Equal(got, []string{"Serve"}) {
				t.Errorf("CachedEntrypoints = %v, want only Serve", got)
			}
			if got := built.worker.DurableObjects; !slices.Equal(got, []string{"PurgeQueue"}) {
				t.Errorf("DurableObjects = %v, want only PurgeQueue", got)
			}
		})
	}
}

func TestEveryEntryWorkerRunsTheEntryBundleAndHoldsTheOriginsReachAsOneSecret(t *testing.T) {
	for name, built := range everyEntry(t) {
		t.Run(name, func(t *testing.T) {
			if string(built.worker.Main.Content) != string(entryBundle) {
				t.Error("Main is not the entry bundle this edge was built with")
			}
			var reach edge.OriginReach
			if err := json.Unmarshal([]byte(built.worker.Secrets[edge.OriginReachVar]), &reach); err != nil {
				t.Fatalf("Secrets[%s] = %q: %v", edge.OriginReachVar, built.worker.Secrets[edge.OriginReachVar], err)
			}
			if reach.Reach != signedReach().Reach || *reach.Store != *signedReach().Store {
				t.Errorf("the reach the worker holds = %+v, want the one the origin wrote", reach)
			}
			for name, value := range built.worker.Variables {
				if strings.Contains(value, "AKIA") || strings.Contains(value, "secretAccessKey") {
					t.Errorf("Variables[%s] holds the origin's key in the clear", name)
				}
			}
		})
	}
}

func TestTheSharedPreviewEntryProgramBindsTheStoreWorkerAndTheBaseDomain(t *testing.T) {
	built := buildEntry(t, environment.TierPreview, "", previewProgrammed())

	if built.name != previewEntryScript {
		t.Errorf("name = %q, want %q", built.name, previewEntryScript)
	}
	if built.worker.Services[genericStoreBinding] != "ocel-preview-releases-store" {
		t.Errorf("Services = %v, want the preview store bound", built.worker.Services)
	}
	for name, want := range map[string]string{
		envPreview:           "1",
		envPreviewGlobal:     "1",
		envPreviewBaseDomain: "preview.acme.com",
	} {
		if built.worker.Variables[name] != want {
			t.Errorf("Variables[%s] = %q, want %q", name, built.worker.Variables[name], want)
		}
	}
	if built.worker.Secrets[edge.PreviewKeyVar] != string(previewKey) {
		t.Errorf("Secrets[%s] missing, want the key that signs preview hostnames delivered as a secret", edge.PreviewKeyVar)
	}
}

func TestEntryProgramRefusesAPreviewEntryWithNoStoreWorker(t *testing.T) {
	program := previewProgrammed()
	program.ReleasesStore.Name = ""

	_, err := newCloudflare(defaultNamespace, Options{}).buildEntryProgram(environment.TierPreview, "", program)
	if err == nil || !strings.Contains(err.Error(), provider.BootstrapCommand(environment.TierPreview)) {
		t.Fatalf("buildEntryProgram = %v, want a refusal naming the bootstrap that provisions the store", err)
	}
}

func TestEntryProgramRefusesAWorkerThatVerifiesPreviewLabelsWithNoKey(t *testing.T) {
	for name, slug := range map[string]string{"the shared preview entry": "", "a project on its own preview domain": "proj"} {
		t.Run(name, func(t *testing.T) {
			program := previewProgrammed()
			program.PreviewKey = ""
			if _, err := newCloudflare(defaultNamespace, Options{}).buildEntryProgram(environment.TierPreview, slug, program); err == nil {
				t.Fatal("buildEntryProgram succeeded, want a worker that can verify no preview hostname refused")
			}
		})
	}
}

func TestAPreviewProjectEntryProgramNamesItsWorkerAndReadsTheStoreOverItsEndpoint(t *testing.T) {
	built := buildEntry(t, environment.TierPreview, "proj", previewProgrammed())

	if built.name != "ocel-preview-entry-proj-b3703d" {
		t.Errorf("name = %q, want the project's preview worker", built.name)
	}
	if built.worker.Variables[envPreview] != "1" || built.worker.Variables[envPreviewBaseDomain] != "preview.acme.com" {
		t.Errorf("Variables = %v, want the preview base domain", built.worker.Variables)
	}
	if built.worker.Secrets[edge.PreviewKeyVar] != string(previewKey) {
		t.Errorf("Secrets[%s] missing, want the preview key", edge.PreviewKeyVar)
	}
	if _, bound := built.worker.Services[genericStoreBinding]; bound {
		t.Errorf("Services = %v, want no store binding: a project worker reads the store over its endpoint", built.worker.Services)
	}
	if built.store.Endpoint != "https://store.example" || built.store.BootstrapCredential != "store-cred" {
		t.Errorf("store = %+v, want the one the origin adopted", built.store)
	}
}

func TestEntryProgramForAPreviewProjectOnTheSharedWildcardCarriesNoBaseDomain(t *testing.T) {
	built := buildEntry(t, environment.TierPreview, "proj", programmed())

	if _, set := built.worker.Variables[envPreviewBaseDomain]; set {
		t.Errorf("Variables has %s, want none: the shared preview entry has the base domain, not the project's worker", envPreviewBaseDomain)
	}
	if built.worker.Variables[envPreview] != "1" {
		t.Errorf("Variables[%s] = %q, want 1", envPreview, built.worker.Variables[envPreview])
	}
}

func TestAProductionProjectEntryProgramCarriesNoPreviewConfig(t *testing.T) {
	built := buildEntry(t, environment.TierProduction, "proj", programmed())

	if built.name != "ocel-prod-entry-proj-034dc7" {
		t.Errorf("name = %q, want ocel-prod-entry-proj-034dc7", built.name)
	}
	for _, unwanted := range []string{envPreview, envPreviewGlobal, envPreviewBaseDomain} {
		if _, set := built.worker.Variables[unwanted]; set {
			t.Errorf("Variables has %s, which belongs to a preview", unwanted)
		}
	}
	if _, set := built.worker.Secrets[edge.PreviewKeyVar]; set {
		t.Errorf("Secrets has %s, which belongs to a preview", edge.PreviewKeyVar)
	}
}

func TestAnEntryWorkerReachingItsOriginByClientCertificateBindsItForMutualTLS(t *testing.T) {
	program := programmed()
	program.Origin = certifiedReach()

	built := buildEntry(t, environment.TierProduction, "shop", program)
	want := map[string]string{originClientCertificateBinding: "c1"}
	if !maps.Equal(built.worker.ClientCertificates, want) {
		t.Errorf("ClientCertificates = %v, want %v", built.worker.ClientCertificates, want)
	}
}

func TestAnEntryWorkerReachingItsOriginBySigningBindsNoClientCertificate(t *testing.T) {
	built := buildEntry(t, environment.TierProduction, "shop", programmed())
	if built.worker.ClientCertificates != nil {
		t.Errorf("ClientCertificates = %v, want nil", built.worker.ClientCertificates)
	}
}

func TestAnEntryWorkerRefusesAnOriginReachedTwoWays(t *testing.T) {
	program := programmed()
	program.Origin.Reach.Certificate = "c1"

	_, err := newCloudflare(defaultNamespace, Options{}).buildEntryProgram(environment.TierProduction, "shop", program)
	if err == nil || !strings.Contains(err.Error(), "one way") {
		t.Fatalf("buildEntryProgram = %v, want a refusal of an origin reached both by signing and by a certificate", err)
	}
}

func TestAnEntryWorkerRefusesAStackHandedNoProgram(t *testing.T) {
	_, err := newCloudflare(defaultNamespace, Options{}).buildEntryProgram(environment.TierProduction, "shop", nil)
	if err == nil || !strings.Contains(err.Error(), string(Kind)) {
		t.Fatalf("buildEntryProgram = %v, want a refusal naming the edge", err)
	}
}

func TestNoWorkerReceivesGateKeys(t *testing.T) {
	for name, built := range everyEntry(t) {
		t.Run(name, func(t *testing.T) {
			for secret := range built.worker.Secrets {
				if strings.Contains(strings.ToUpper(secret), "GATE") {
					t.Errorf("Secrets carries %s, want none: the gate is published to the releases store, where a rotation takes effect without a deploy", secret)
				}
			}
		})
	}
}
