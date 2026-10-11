package deploy

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/edge"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestTheReleaseHandsBackTheEdgeDeliveryOnlyItKnows(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	cfg.Objects = &fakeArtifactStore{}
	cfg.EdgeAccessKeyID, cfg.EdgeSecretKey = "AKIAEDGE", "edge-secret"
	cfg.PurgeSeed = "a-seed"

	bundlePath := filepath.Join(cfg.ArtifactRoot, "apps", "web", filepath.FromSlash(edge.AppBundleFile))
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	spec.App.VendorState = appBundle{Envelope: "an-envelope", Ciphertext: []byte("sealed")}
	spec.App.EdgeRouteTable = edgeRouteTable(appCoordinate(spec).RouteTableKey("ab"))

	release := releasing(t, cfg)
	work, err := release.appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	spec.VendorState = work

	outputs := auto.OutputMap{"fn--web--entry": auto.OutputValue{Value: map[string]any{
		outputKeyFunctionURL:  "https://web.lambda-url.us-east-1.on.aws/",
		outputKeyFunctionName: "shop-prod-web-entry",
	}}, "fn--web--admin": auto.OutputValue{Value: map[string]any{
		outputKeyFunctionURL:  "https://admin.lambda-url.us-east-1.on.aws/",
		outputKeyFunctionName: "shop-prod-web-admin",
	}}}

	result, err := release.decodeApp(spec, outputs)
	if err != nil {
		t.Fatalf("decodeApp() = %v", err)
	}
	if want := appEdgeBundleKey(appCoordinate(spec)); result.EdgeBundleKey != want {
		t.Errorf("EdgeBundleKey = %q, want %q: the record the edge loads code by names the key only the vendor's upload knows",
			result.EdgeBundleKey, want)
	}
	if result.RouteTableKey != spec.App.EdgeRouteTable.Location.Key {
		t.Errorf("RouteTableKey = %q, want %q: the record the edge dispatches by names the table this release uploaded",
			result.RouteTableKey, spec.App.EdgeRouteTable.Location.Key)
	}
	if result.Envelope != "an-envelope" {
		t.Errorf("Envelope = %q, want the one sealed beside the bundle", result.Envelope)
	}
	if result.PurgeSecret != cloudflare.DerivePurgeSecret(cfg.PurgeSeed, spec.App.ISR.Prefix) {
		t.Errorf("PurgeSecret = %q, want the secret the edge accepts a purge signed with", result.PurgeSecret)
	}
}

func TestAReleaseThatUploadsNoEdgeBundleHandsBackNoKey(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	release := releasing(t, cfg)
	work, err := release.appWork(spec, nil)
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	spec.VendorState = work

	result, err := release.decodeApp(spec, auto.OutputMap{
		"fn--web--entry": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://web.example/"}},
		"fn--web--admin": auto.OutputValue{Value: map[string]any{outputKeyFunctionURL: "https://admin.example/"}},
	})
	if err != nil {
		t.Fatalf("decodeApp() = %v", err)
	}
	if result.EdgeBundleKey != "" || result.Envelope != "" || result.RouteTableKey != "" {
		t.Errorf("EdgeBundleKey = %q, Envelope = %q, RouteTableKey = %q, want nothing named where nothing was uploaded",
			result.EdgeBundleKey, result.Envelope, result.RouteTableKey)
	}
	if result.PurgeSecret != "" {
		t.Errorf("PurgeSecret = %q, want nothing where the account adopts no cache store", result.PurgeSecret)
	}
}

func TestAReleaseNamesEverySecretItsProgramsCarrySoTheEngineCanMaskThem(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	cfg.OriginSecret, cfg.PreviousOriginSecret = "origin-now", "origin-before"
	cfg.PurgeSeed = "a-seed"

	got := releasing(t, cfg).Secrets(spec)
	for _, want := range []string{"origin-now", "origin-before", "a-seed", cloudflare.DerivePurgeSecret(cfg.PurgeSeed, spec.App.ISR.Prefix)} {
		if !slices.Contains(got, want) {
			t.Errorf("the release names %d secrets and not %q, want every secret its programs carry", len(got), want)
		}
	}
}
