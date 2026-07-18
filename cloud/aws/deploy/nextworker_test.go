package deploy

import (
	"context"
	"os"
	"path/filepath"
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

func TestBuildFunctionURLs_KeysByRouteIDForNextFunctions(t *testing.T) {
	functions := []*deploymentsv1.ManifestFunction{
		{LogicalName: "api_documents", Framework: "next", RouteId: "/api/documents"},
		{LogicalName: "index", Framework: "next", RouteId: "/"},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("api_documents", "https://a.lambda-url.aws/"),
		fnOutput("index", "https://b.lambda-url.aws/"),
	}

	got := buildFunctionURLs(functions, outputs)

	want := map[string]string{
		"/api/documents": "https://a.lambda-url.aws/",
		"/":              "https://b.lambda-url.aws/",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
}

func TestBuildFunctionURLs_SkipsNonNextAndUnresolvedFunctions(t *testing.T) {
	functions := []*deploymentsv1.ManifestFunction{
		{LogicalName: "web_api", Framework: "express", RouteId: ""},
		{LogicalName: "orphan", Framework: "next", RouteId: "/orphan"},
	}
	outputs := []*deploymentsv1.ResourceOutput{
		fnOutput("web_api", "https://express.lambda-url.aws/"),
		// no output for "orphan"
	}

	got := buildFunctionURLs(functions, outputs)

	if len(got) != 0 {
		t.Fatalf("expected no entries (express is not next, orphan has no URL), got %v", got)
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
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	if got := sanitizeWorkerName(long); len(got) != 63 {
		t.Errorf("expected clamp to 63 chars, got %d", len(got))
	}
}

func TestHashAsset_MatchesWranglerAlgorithm(t *testing.T) {
	// Reference value computed independently:
	//   sha256(base64("hello") + "txt").hex()[:32]
	if got, want := hashAsset([]byte("hello"), "txt"), "129d0bf9c674d4cc340cf5f8feeb9f36"; got != want {
		t.Fatalf("hashAsset = %q, want %q", got, want)
	}
	if len(hashAsset([]byte("anything"), "")) != 32 {
		t.Errorf("hash must be 32 hex chars")
	}
}

func TestCollectStaticAssets_ReadsFilesWithHashAndSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "next.svg"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icons", "logo.png"), []byte("xx"), 0o644); err != nil {
		t.Fatal(err)
	}

	assets, err := collectStaticAssets(dir)
	if err != nil {
		t.Fatalf("collectStaticAssets: %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("got %d assets, want 2", len(assets))
	}

	byPath := map[string]edge.StaticAsset{}
	for _, a := range assets {
		byPath[a.Path] = a
	}
	svg, ok := byPath["/next.svg"]
	if !ok {
		t.Fatalf("missing /next.svg; got %v", byPath)
	}
	if svg.Size != 5 {
		t.Errorf("/next.svg size = %d, want 5", svg.Size)
	}
	if svg.Hash != hashAsset([]byte("hello"), "svg") {
		t.Errorf("/next.svg hash = %q, want hashAsset of its contents+ext", svg.Hash)
	}
	if _, ok := byPath["/icons/logo.png"]; !ok {
		t.Errorf("missing nested /icons/logo.png; got %v", byPath)
	}
}

func TestCollectStaticAssets_MissingDirYieldsNone(t *testing.T) {
	assets, err := collectStaticAssets(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("expected no error for missing dir, got %v", err)
	}
	if len(assets) != 0 {
		t.Errorf("expected no assets, got %d", len(assets))
	}
}

// recordingEdge captures the AppDeployment it is handed so orchestration can be
// asserted without touching any real edge API.
type recordingEdge struct {
	got    edge.AppDeployment
	called bool
}

func (f *recordingEdge) Bootstrap(context.Context) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

func (f *recordingEdge) DeployApp(_ context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	f.got = app
	f.called = true
	return edge.AppResult{URL: "https://ocel-proj-prod.acme.workers.dev"}, nil
}

func TestDeployNextWorker_NoNextFunction_IsNoOp(t *testing.T) {
	fake := &recordingEdge{}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_api", Framework: "express"},
		},
	}

	out, err := deployNextWorker(context.Background(), Config{Edge: fake}, manifest, nil, nil)
	if err != nil {
		t.Fatalf("deployNextWorker: %v", err)
	}
	if fake.called {
		t.Error("deployer should not be called when there is no Next function")
	}
	if out != nil {
		t.Errorf("expected no outputs, got %v", out)
	}
}

func TestDeployNextWorker_AssemblesUploadAndReportsURL(t *testing.T) {
	artifactRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactRoot, "routing-manifest.json"), []byte(`{"buildId":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(artifactRoot, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "static", "next.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	workerBundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(workerBundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envNextWorkerPath, workerBundle)

	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "api_documents", Framework: "next", RouteId: "/api/documents"},
		},
	}
	outputs := []*deploymentsv1.ResourceOutput{fnOutput("api_documents", "https://fn.lambda-url.aws/")}

	out, err := deployNextWorker(context.Background(), cfg, manifest, outputs, nil)
	if err != nil {
		t.Fatalf("deployNextWorker: %v", err)
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
	if got, want := up.Worker.Vars[nextWorkerURLsVar], `{"/api/documents":"https://fn.lambda-url.aws/"}`; got != want {
		t.Errorf("FUNCTION_URLS = %q, want %q", got, want)
	}
	if len(out) != 1 || out[0].GetFunction().GetUrl() != "https://ocel-proj-prod.acme.workers.dev" {
		t.Errorf("expected the worker URL output, got %v", out)
	}
}

func TestDeployNextWorker_InjectsInterceptionBindingsWhenEdgeCredsPresent(t *testing.T) {
	artifactRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactRoot, "routing-manifest.json"), []byte(`{"buildId":"b1","appName":"web"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workerBundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(workerBundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envNextWorkerPath, workerBundle)

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

	if _, err := deployNextWorker(context.Background(), cfg, manifest, nil, nil); err != nil {
		t.Fatalf("deployNextWorker: %v", err)
	}

	up := fake.got
	// The secret access key must be a secret_text binding, never a plain var.
	if up.Worker.Secrets[edgeSecretKeyVar] != "secret-edge" {
		t.Errorf("%s secret = %q, want secret-edge", edgeSecretKeyVar, up.Worker.Secrets[edgeSecretKeyVar])
	}
	if _, leaked := up.Worker.Vars[edgeSecretKeyVar]; leaked {
		t.Error("the secret access key must not appear in plain-text Vars")
	}

	wantVars := map[string]string{
		edgeAccessKeyIDVar:       "AKIAEDGE",
		edgeRegionVar:            "us-west-2",
		"OCEL_ISR_BUCKET":        "ocel-assets",
		"OCEL_STATE_TABLE":       "ocel-state",
		"OCEL_ISR_PREFIX":        "prod/proj/web/b1",
		"OCEL_ISR_TAG_NAMESPACE": "TAG#prod#proj#web#b1#",
	}
	for k, want := range wantVars {
		if got := up.Worker.Vars[k]; got != want {
			t.Errorf("Vars[%s] = %q, want %q", k, got, want)
		}
	}
}

func TestDeployNextWorker_NoInterceptionBindingsWithoutEdgeCreds(t *testing.T) {
	artifactRoot := writeMinimalWorkerArtifacts(t)
	fake := &recordingEdge{}
	cfg := Config{Edge: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "index", Framework: "next", RouteId: "/"}},
	}

	if _, err := deployNextWorker(context.Background(), cfg, manifest, nil, nil); err != nil {
		t.Fatalf("deployNextWorker: %v", err)
	}

	if fake.got.Worker.Secrets != nil {
		t.Errorf("expected no secrets without edge creds, got %v", fake.got.Worker.Secrets)
	}
	if _, ok := fake.got.Worker.Vars[edgeAccessKeyIDVar]; ok {
		t.Error("expected no edge access key var without edge creds")
	}
}

func TestDeployNextWorker_CustomDomainOnlyForProduction(t *testing.T) {
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

			if _, err := deployNextWorker(context.Background(), cfg, manifest, nil, nil); err != nil {
				t.Fatalf("deployNextWorker: %v", err)
			}
			if fake.got.Domain != tc.want {
				t.Errorf("Domain = %q, want %q", fake.got.Domain, tc.want)
			}
		})
	}
}

func writeMinimalWorkerArtifacts(t *testing.T) string {
	t.Helper()
	artifactRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactRoot, "routing-manifest.json"), []byte(`{"buildId":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	workerBundle := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(workerBundle, []byte("export default {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envNextWorkerPath, workerBundle)
	return artifactRoot
}
