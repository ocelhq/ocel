package deploy

import (
	"maps"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const previewKey edge.PreviewKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func awsWorkerValues() WorkerValues {
	return WorkerValues{
		ImageOptimizerURL:  "https://optimizer.example",
		RevalidateQueueURL: "https://queue.example",
		AssetBucket:        "assets-bucket",
		Region:             "eu-west-2",
		EdgeAccessKeyID:    "AKIA",
		EdgeSecretKey:      "secret",
	}
}

func programmed(slug string, tier environment.Tier) cloudflare.EntryProgram {
	return cloudflare.EntryProgram{
		Tier:                     tier,
		Entry:                    edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Namespace:                defaultNamespace,
		Slug:                     slug,
		Env:                      "prod",
		Origin:                   awsWorkerValues().Bindings(),
		Values:                   map[string]string{"cacheBucket": "ocel-edge-cache-preview"},
		StoreScriptName:          "ocel-releases-store-preview",
		StoreEndpoint:            "https://store.example",
		StoreBootstrapCredential: "store-cred",
		ISRWriterScriptName:      "ocel-isr-writer-preview",
	}
}

func TestTheAWSEntryWorkerKeepsItsNamesVariablesSecretsAndBindings(t *testing.T) {
	entry := edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")}
	awsVariables := map[string]string{
		"OCEL_IMAGE_OPTIMIZER_URL":  "https://optimizer.example",
		"OCEL_REVALIDATE_QUEUE_URL": "https://queue.example",
		"OCEL_ASSET_BUCKET":         "assets-bucket",
		"OCEL_AWS_REGION":           "eu-west-2",
		"OCEL_EDGE_ACCESS_KEY_ID":   "AKIA",
	}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		maps.Copy(out, awsVariables)
		maps.Copy(out, extra)
		return out
	}
	values := map[string]string{"cacheBucket": "ocel-edge-cache-preview"}
	key := string(previewKey)

	production := programmed("proj", environment.TierProduction)
	previewProject := programmed("proj", environment.TierPreview)
	previewProject.PreviewBaseDomain = "preview.acme.com"
	previewProject.PreviewKey = previewKey
	sharedEntry := programmed("", environment.TierPreview)
	sharedEntry.PreviewBaseDomain = "preview.acme.com"
	sharedEntry.PreviewKey = previewKey

	for name, tc := range map[string]struct {
		program cloudflare.EntryProgram
		want    provider.EdgeProgram
	}{
		"a production project": {production, provider.EdgeProgram{Values: values, Spec: &edge.ProgramSpec{
			Name:                "ocel--proj--prod--root",
			Worker:              edge.Worker{CachedEntrypoints: []string{"Serve"}, Main: entry, Variables: with(nil), Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "secret"}},
			StoreScriptName:     "ocel-releases-store-preview",
			StoreEndpoint:       "https://store.example",
			BootstrapCredential: "store-cred",
			ISRWriterScriptName: "ocel-isr-writer-preview",
		}}},
		"a preview project": {previewProject, provider.EdgeProgram{Values: values, Spec: &edge.ProgramSpec{
			Name:                "ocel--proj--preview--root",
			PruneWorkerStem:     "ocel--proj--preview",
			Worker:              edge.Worker{CachedEntrypoints: []string{"Serve"}, Main: entry, Variables: with(map[string]string{"OCEL_PREVIEW": "1", "OCEL_PREVIEW_BASE_DOMAIN": "preview.acme.com"}), Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "secret", "OCEL_PREVIEW_KEY": key}},
			StoreScriptName:     "ocel-releases-store-preview",
			StoreEndpoint:       "https://store.example",
			BootstrapCredential: "store-cred",
			ISRWriterScriptName: "ocel-isr-writer-preview",
		}}},
		"the shared preview entry": {sharedEntry, provider.EdgeProgram{Values: values, Spec: &edge.ProgramSpec{
			Worker: edge.Worker{
				CachedEntrypoints: []string{"Serve"},
				Main:              entry,
				Variables:         with(map[string]string{"OCEL_PREVIEW": "1", "OCEL_PREVIEW_GLOBAL": "1", "OCEL_PREVIEW_BASE_DOMAIN": "preview.acme.com"}),
				Secrets:           map[string]string{"OCEL_EDGE_SECRET_KEY": "secret", "OCEL_PREVIEW_KEY": key},
				Services:          map[string]string{"RELEASES": "ocel-releases-store-preview"},
			},
			StoreScriptName:     "ocel-releases-store-preview",
			StoreEndpoint:       "https://store.example",
			BootstrapCredential: "store-cred",
			ISRWriterScriptName: "ocel-isr-writer-preview",
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.program.Build()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Build() = %+v (spec %+v), want %+v (spec %+v)", got, got.Spec, tc.want, tc.want.Spec)
			}
		})
	}
}

func TestWorkerValuesBindEveryValueTheEntryWorkerReads(t *testing.T) {
	got := awsWorkerValues().Bindings()

	wantVariables := map[string]string{
		edge.ImageOptimizerURLVar:  "https://optimizer.example",
		edge.RevalidateQueueURLVar: "https://queue.example",
		edge.AssetBucketVar:        "assets-bucket",
		edge.AWSRegionVar:          "eu-west-2",
		edge.EdgeAccessKeyIDVar:    "AKIA",
	}
	if !maps.Equal(got.Variables, wantVariables) {
		t.Errorf("Variables = %v, want %v", got.Variables, wantVariables)
	}
	if wantSecrets := map[string]string{edge.EdgeSecretKeyVar: "secret"}; !maps.Equal(got.Secrets, wantSecrets) {
		t.Errorf("Secrets = %v, want %v", got.Secrets, wantSecrets)
	}
}

func TestWorkerValuesBindNoEdgeKeyNorAssetBucketWithoutBothHalves(t *testing.T) {
	for name, mutate := range map[string]func(*WorkerValues){
		"a key id with no secret": func(f *WorkerValues) { f.EdgeSecretKey = "" },
		"a secret with no key id": func(f *WorkerValues) { f.EdgeAccessKeyID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			values := awsWorkerValues()
			mutate(&values)

			got := values.Bindings()
			if _, set := got.Variables[edge.EdgeAccessKeyIDVar]; set {
				t.Errorf("Variables has %s, want it left out without both halves", edge.EdgeAccessKeyIDVar)
			}
			if got.Secrets != nil {
				t.Errorf("Secrets = %v, want nil", got.Secrets)
			}
			for _, name := range []string{edge.AssetBucketVar, edge.AWSRegionVar} {
				if _, set := got.Variables[name]; set {
					t.Errorf("Variables has %s, want it left out: the edge cannot sign a read of the bucket without its key", name)
				}
			}
		})
	}
}

func TestWorkerValuesLeaveOutValuesTheBootstrapDidNotRecord(t *testing.T) {
	values := awsWorkerValues()
	values.ImageOptimizerURL = ""

	got := values.Bindings()
	if _, set := got.Variables[edge.ImageOptimizerURLVar]; set {
		t.Errorf("Variables has %s = %q, want none", edge.ImageOptimizerURLVar, got.Variables[edge.ImageOptimizerURLVar])
	}
}

func TestWorkerValuesLeaveOutTheAssetBucketWithoutItsRegion(t *testing.T) {
	values := awsWorkerValues()
	values.Region = ""

	got := values.Bindings()
	if _, set := got.Variables[edge.AssetBucketVar]; set {
		t.Errorf("Variables has %s, want it left out: the edge cannot sign a read without the bucket's region", edge.AssetBucketVar)
	}
}
