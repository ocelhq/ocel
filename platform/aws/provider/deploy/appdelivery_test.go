package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestTheReleaseHandsBackTheEdgeDeliveryOnlyItKnows(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	cfg.CacheStoreBucket = "isr"
	cfg.CacheStoreUploader = &fakeUploader{}
	cfg.ISRWriterEndpoint = "https://writer.example"
	cfg.ISRWriterSeed = "a-seed"

	bundlePath := filepath.Join(cfg.ArtifactRoot, "apps", "web", filepath.FromSlash(edge.AppBundleFile))
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan.App.Packed = appBundle{Envelope: "an-envelope", Ciphertext: []byte("sealed")}

	release := releasing(t, cfg)
	work, err := release.appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	plan.Options = work

	outputs := auto.OutputMap{"fn--web--entry": auto.OutputValue{Value: map[string]any{
		outputKeyFunctionURL:  "https://web.lambda-url.us-east-1.on.aws/",
		outputKeyFunctionName: "shop-prod-web-entry",
	}}, "fn--web--admin": auto.OutputValue{Value: map[string]any{
		outputKeyFunctionURL:  "https://admin.lambda-url.us-east-1.on.aws/",
		outputKeyFunctionName: "shop-prod-web-admin",
	}}}

	result, err := release.decodeApp(plan, outputs)
	if err != nil {
		t.Fatalf("decodeApp() = %v", err)
	}
	if want := appEdgeBundleKey(appCoordinate(plan)); result.EdgeBundleKey != want {
		t.Errorf("EdgeBundleKey = %q, want %q: the record the edge loads code by names the key only the vendor's upload knows",
			result.EdgeBundleKey, want)
	}
	if result.Envelope != "an-envelope" {
		t.Errorf("Envelope = %q, want the one sealed beside the bundle", result.Envelope)
	}
	if result.ISRWriteSecret != isrWriteSecret(cfg.ISRWriterSeed, plan.App.ISR.Prefix) {
		t.Errorf("ISRWriteSecret = %q, want the secret the revalidation writer demands", result.ISRWriteSecret)
	}
}

func TestAReleaseThatUploadsNoEdgeBundleHandsBackNoKey(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedAppStack(t)
	release := releasing(t, cfg)
	work, err := release.appWork(plan, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	plan.Options = work

	result, err := release.decodeApp(plan, auto.OutputMap{
		"fn--web--entry": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://web.example/"}},
		"fn--web--admin": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://admin.example/"}},
	})
	if err != nil {
		t.Fatalf("decodeApp() = %v", err)
	}
	if result.EdgeBundleKey != "" || result.Envelope != "" {
		t.Errorf("EdgeBundleKey = %q, Envelope = %q, want nothing named where no bundle was uploaded",
			result.EdgeBundleKey, result.Envelope)
	}
	if result.ISRWriteSecret != "" {
		t.Errorf("ISRWriteSecret = %q, want nothing where the account adopts no cache store", result.ISRWriteSecret)
	}
}
