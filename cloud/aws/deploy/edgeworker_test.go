package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func fnOutput(logicalName, url string) *deploymentsv1.ResourceOutput {
	return &deploymentsv1.ResourceOutput{
		LogicalName: logicalName,
		Output: &deploymentsv1.ResourceOutput_Function{
			Function: &deploymentsv1.FunctionOutput{Url: url},
		},
	}
}

func TestSanitizeWorkerName(t *testing.T) {
	cases := map[string]string{
		"ocel-proj_ABC-prod": "ocel-proj-abc-prod",
		"ocel-Proj.123":      "ocel-proj-123",
		"--weird__name--":    "weird-name",
		"":                   "ocel-worker",
		"////":               "ocel-worker",
	}
	for in, want := range cases {
		if got := sanitizeWorkerName(in); got != want {
			t.Errorf("sanitizeWorkerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeWorkerName_ClampsTo63Chars(t *testing.T) {
	long := strings.Repeat("a", 100)
	if got := sanitizeWorkerName(long); len(got) != 63 {
		t.Errorf("expected clamp to 63 chars, got %d", len(got))
	}
}

// recordingEdge captures the AppDeployment it is handed so orchestration can be
// asserted without touching any real edge API.
type recordingEdge struct {
	got    edge.AppDeployment
	called bool
}

func (f *recordingEdge) Kind() edge.Kind { return edge.KindCloudflare }

func (f *recordingEdge) Bootstrap(context.Context) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (f *recordingEdge) DeployApp(_ context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	f.got = app
	f.called = true
	return edge.AppResult{URL: "https://ocel-proj-prod.acme.workers.dev"}, nil
}

// otherEdge stands in for a future provider-native edge no framework has
// registered a worker for.
type otherEdge struct{ recordingEdge }

func (o *otherEdge) Kind() edge.Kind { return "provider-native" }

func TestDeployEdgeWorker_FrameworkWithNoWorkerIsANoOp(t *testing.T) {
	fake := &recordingEdge{}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_api", Framework: "express"},
		},
	}

	out, err := deployEdgeWorker(context.Background(), Config{Edge: fake}, manifest, nil, nil)
	if err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	if fake.called {
		t.Error("a framework registering no worker must not reach the edge")
	}
	if out != nil {
		t.Errorf("expected no outputs, got %v", out)
	}
}

func TestDeployEdgeWorker_AssemblesUploadAndReportsURL(t *testing.T) {
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, `{"buildId":"b"}`)
	if err := os.MkdirAll(filepath.Join(artifactRoot, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "static", "next.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	setWorkerBundle(t)

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "api_documents", Framework: "next", RouteId: "/api/documents"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("api_documents", "https://fn.lambda-url.aws/")}

	out, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil)
	if err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}
	if !fake.called {
		t.Fatal("expected the deployer to be called")
	}

	up := fake.got
	if up.Name != "ocel-proj-1-prod" {
		t.Errorf("Name = %q, want ocel-proj-1-prod", up.Name)
	}
	if string(up.Worker.Main.Content) != "export default {}" {
		t.Errorf("Main content = %q", up.Worker.Main.Content)
	}
	if len(up.Worker.Modules) != 1 || up.Worker.Modules[0].Name != "routing-manifest.json" {
		t.Errorf("expected the routing manifest module, got %v", up.Worker.Modules)
	}
	if up.Worker.Modules[0].ContentType != "text/plain" {
		t.Errorf("manifest module Content-Type = %q, want text/plain (no JSON module type exists)", up.Worker.Modules[0].ContentType)
	}
	if len(up.Worker.Assets) != 1 || up.Worker.Assets[0].Path != "/next.svg" {
		t.Errorf("expected the static asset, got %v", up.Worker.Assets)
	}
	if got, want := up.Worker.Vars["FUNCTION_URLS"], `{"/api/documents":"https://fn.lambda-url.aws/"}`; got != want {
		t.Errorf("FUNCTION_URLS = %q, want %q", got, want)
	}
	if len(out) != 1 || out[0].GetFunction().GetUrl() != "https://ocel-proj-prod.acme.workers.dev" {
		t.Errorf("expected the worker URL output, got %v", out)
	}
}

// The exact binding set a fully-configured Next app emits. Dropping any one of
// the OCEL_ISR_* / OCEL_EDGE_* bindings silently degrades the worker to
// forwarding every prerender route to the Lambda — still correct, just slower
// and costlier — so only pinning the whole set catches it.
func TestDeployEdgeWorker_FullyConfiguredBindingSet(t *testing.T) {
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, `{"buildId":"b1","appName":"web"}`)
	setWorkerBundle(t)

	fake := &recordingEdge{}
	cfg := Config{
		Edge:            fake,
		ArtifactRoot:    artifactRoot,
		StackName:       "proj_1-prod",
		Region:          "us-west-2",
		AssetBucket:     "ocel-assets",
		StateTable:      "ocel-state",
		Env:             "prod",
		EdgeAccessKeyID: "AKIAEDGE",
		EdgeSecretKey:   "secret-edge",
	}
	manifest := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "index", Framework: "next", RouteId: "/"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("index", "https://fn.lambda-url.aws/")}

	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("deployEdgeWorker: %v", err)
	}

	wantVars := map[string]string{
		"FUNCTION_URLS":           `{"/":"https://fn.lambda-url.aws/"}`,
		"OCEL_EDGE_ACCESS_KEY_ID": "AKIAEDGE",
		"OCEL_AWS_REGION":         "us-west-2",
		"OCEL_ISR_BUCKET":         "ocel-assets",
		"OCEL_ISR_PREFIX":         "prod/proj/web/b1",
		"OCEL_STATE_TABLE":        "ocel-state",
		"OCEL_STATE_TABLE_INDEX":  "gsi1",
		"OCEL_ISR_TAG_NAMESPACE":  "TAG#prod#proj#web#b1#",
	}
	up := fake.got
	if len(up.Worker.Vars) != len(wantVars) {
		t.Errorf("got %d vars, want %d: %v", len(up.Worker.Vars), len(wantVars), up.Worker.Vars)
	}
	for k, want := range wantVars {
		if got := up.Worker.Vars[k]; got != want {
			t.Errorf("Vars[%s] = %q, want %q", k, got, want)
		}
	}
	if len(up.Worker.Secrets) != 1 || up.Worker.Secrets["OCEL_EDGE_SECRET_KEY"] != "secret-edge" {
		t.Errorf("Secrets = %v, want only OCEL_EDGE_SECRET_KEY", up.Worker.Secrets)
	}
	if _, leaked := up.Worker.Vars["OCEL_EDGE_SECRET_KEY"]; leaked {
		t.Error("the secret access key must not appear in plain-text Vars")
	}
	if up.Worker.AssetBinding != "ASSETS" {
		t.Errorf("AssetBinding = %q, want ASSETS", up.Worker.AssetBinding)
	}
}

func TestDeployEdgeWorker_NoCacheBindingsWithoutEdgeCreds(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", RouteId: "/"}},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("index", "https://fn.lambda-url.aws/")}

	if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
		t.Fatalf("a substrate predating edge credentials must still deploy: %v", err)
	}

	if fake.got.Worker.Secrets != nil {
		t.Errorf("expected no secrets without edge creds, got %v", fake.got.Worker.Secrets)
	}
	if len(fake.got.Worker.Vars) != 1 || fake.got.Worker.Vars["FUNCTION_URLS"] == "" {
		t.Errorf("expected only FUNCTION_URLS, got %v", fake.got.Worker.Vars)
	}
}

// The two ways a cache legitimately does not exist. Both must read as
// not-configured, never as an error, or the deploy would fail where today it
// degrades to forwarding.
func TestDeployResolver_CacheStoreNotConfigured(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	withCreds := Config{ArtifactRoot: artifactRoot, EdgeAccessKeyID: "AKIAEDGE", EdgeSecretKey: "secret-edge"}
	nextApp := &deploymentsv1.Manifest{
		ProjectId: "proj",
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", RouteId: "/"}},
	}

	cases := map[string]*deployResolver{
		"substrate predates edge credentials": {cfg: Config{ArtifactRoot: artifactRoot}, manifest: nextApp},
		"app has no prerendered content":      {cfg: withCreds, manifest: &deploymentsv1.Manifest{ProjectId: "proj"}},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			store, configured, err := r.CacheStore()
			if err != nil {
				t.Fatalf("not-configured must not be an error: %v", err)
			}
			if configured {
				t.Errorf("expected not-configured, got %+v", store)
			}
		})
	}
}

func TestDeployEdgeWorker_UnresolvableRouteFailsNamingIt(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "orphan", Framework: "next", RouteId: "/orphan"}},
	}

	_, err := deployEdgeWorker(context.Background(), cfg, manifest, nil, nil)
	if err == nil {
		t.Fatal("expected an unresolvable route to fail the deploy")
	}
	if !strings.Contains(err.Error(), "/orphan") {
		t.Errorf("error must name the function, got %q", err)
	}
	if fake.called {
		t.Error("a worker that cannot route must never reach the edge")
	}
}

func TestDeployEdgeWorker_UnsupportedPairingNamesBoth(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	cfg := Config{Edge: &otherEdge{}, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", RouteId: "/"}},
	}

	_, err := deployEdgeWorker(context.Background(), cfg, manifest, nil, nil)
	if err == nil {
		t.Fatal("expected an unsupported framework/edge pairing to fail")
	}
	if !strings.Contains(err.Error(), "next") || !strings.Contains(err.Error(), "provider-native") {
		t.Errorf("error must name both framework and edge, got %q", err)
	}
}

func TestDeployEdgeWorker_CustomDomainOnlyForProduction(t *testing.T) {
	cases := []struct {
		name    string
		class   deploymentsv1.Environment_Class
		domains map[string]string
		want    string
	}{
		{"production with domain", deploymentsv1.Environment_CLASS_PRODUCTION, map[string]string{"production": "app.acme.com"}, "app.acme.com"},
		{"production without domain", deploymentsv1.Environment_CLASS_PRODUCTION, nil, ""},
		{"preview ignores domain", deploymentsv1.Environment_CLASS_PREVIEW, map[string]string{"production": "app.acme.com"}, ""},
		{"unspecified ignores domain", deploymentsv1.Environment_CLASS_UNSPECIFIED, map[string]string{"production": "app.acme.com"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifactRoot := writeMinimalWorkerArtifacts(t)
			fake := &recordingEdge{}
			cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod", Class: tc.class}
			manifest := &deploymentsv1.Manifest{
				Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "api_documents", Framework: "next", RouteId: "/api/documents"}},
				Domains:   tc.domains,
			}
			outputs := []*deploymentsv1.ResourceOutput{fnOutput("api_documents", "https://fn.lambda-url.aws/")}

			if _, err := deployEdgeWorker(context.Background(), cfg, manifest, outputs, nil); err != nil {
				t.Fatalf("deployEdgeWorker: %v", err)
			}
			if fake.got.Domain != tc.want {
				t.Errorf("Domain = %q, want %q", fake.got.Domain, tc.want)
			}
		})
	}
}

func writeRoutingManifest(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "routing-manifest.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setWorkerBundle writes a worker bundle and exports the manifest pointing the
// Next-on-Cloudflare pairing at it, standing in for the npm launcher.
func setWorkerBundle(t *testing.T) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(bundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.BundleManifest{edge.FrameworkNext: {edge.KindCloudflare: bundle}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.EnvWorkerBundles, string(raw))
}

func writeMinimalWorkerArtifacts(t *testing.T) string {
	t.Helper()
	artifactRoot := t.TempDir()
	writeRoutingManifest(t, artifactRoot, `{"buildId":"b"}`)
	setWorkerBundle(t)
	return artifactRoot
}
