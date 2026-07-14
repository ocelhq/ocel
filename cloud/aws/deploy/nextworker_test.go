package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"

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
		"ocel-proj_ABC-prod":  "ocel-proj-abc-prod",
		"ocel-Proj.123":       "ocel-proj-123",
		"--weird__name--":     "weird-name",
		"":                    "ocel-worker",
		"////":                "ocel-worker",
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

	byPath := map[string]StaticAsset{}
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

// fakeCloudflare captures the WorkerUpload it is handed so orchestration can be
// asserted without touching the Cloudflare API.
type fakeCloudflare struct {
	got    WorkerUpload
	called bool
}

func (f *fakeCloudflare) DeployWorker(_ context.Context, upload WorkerUpload) (WorkerResult, error) {
	f.got = upload
	f.called = true
	return WorkerResult{URL: "https://ocel-proj-prod.acme.workers.dev"}, nil
}

func TestDeployNextWorker_NoNextFunction_IsNoOp(t *testing.T) {
	fake := &fakeCloudflare{}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "web_api", Framework: "express"},
		},
	}

	out, err := deployNextWorker(context.Background(), Config{Cloudflare: fake}, manifest, nil, nil)
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

	t.Setenv(envCloudflareAccountID, "acct-123")
	t.Setenv(envNextWorkerPath, workerBundle)

	fake := &fakeCloudflare{}
	cfg := Config{Cloudflare: fake, ArtifactRoot: artifactRoot, StackName: "proj_1-prod"}
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
	if up.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want acct-123", up.AccountID)
	}
	if up.ScriptName != "ocel-proj-1-prod" {
		t.Errorf("ScriptName = %q, want ocel-proj-1-prod", up.ScriptName)
	}
	if string(up.Main.Content) != "export default {}" {
		t.Errorf("Main content = %q", up.Main.Content)
	}
	if len(up.Modules) != 1 || up.Modules[0].Name != "routing-manifest.json" {
		t.Errorf("expected the routing manifest module, got %v", up.Modules)
	}
	if len(up.Assets) != 1 || up.Assets[0].Path != "/next.svg" {
		t.Errorf("expected the static asset, got %v", up.Assets)
	}
	if got, want := up.Vars[nextWorkerURLsVar], `{"/api/documents":"https://fn.lambda-url.aws/"}`; got != want {
		t.Errorf("FUNCTION_URLS = %q, want %q", got, want)
	}
	if len(out) != 1 || out[0].GetFunction().GetUrl() != "https://ocel-proj-prod.acme.workers.dev" {
		t.Errorf("expected the worker URL output, got %v", out)
	}
}

// metadataFromMultipart builds the worker upload body and parses its "metadata"
// part back into a map for assertion.
func metadataFromMultipart(t *testing.T, upload WorkerUpload, assetsJWT string) map[string]any {
	t.Helper()
	body, contentType, err := buildScriptMultipart(upload, assetsJWT)
	if err != nil {
		t.Fatalf("buildScriptMultipart: %v", err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		if part.FormName() != "metadata" {
			continue
		}
		data, _ := io.ReadAll(part)
		var meta map[string]any
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("unmarshal metadata: %v", err)
		}
		return meta
	}
	t.Fatal("no metadata part in multipart body")
	return nil
}

func hasAssetBinding(meta map[string]any) bool {
	bindings, _ := meta["bindings"].([]any)
	for _, b := range bindings {
		if m, ok := b.(map[string]any); ok && m["type"] == "assets" {
			return true
		}
	}
	return false
}

func TestBuildScriptMultipart_AssetsBindingGatedOnCompletionJWT(t *testing.T) {
	upload := WorkerUpload{
		ScriptName:   "w",
		AssetBinding: "ASSETS",
		Main:         WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("x")},
		Vars:         map[string]string{"FUNCTION_URLS": "{}"},
		Assets:       []StaticAsset{{Path: "/a.svg", Hash: "h", Size: 1, Content: []byte("a")}},
	}

	// Without a completion JWT (e.g. a redeploy where the session returned no
	// buckets and the token was lost), neither the assets binding nor the assets
	// metadata may appear — Cloudflare rejects a binding without assets.
	noJWT := metadataFromMultipart(t, upload, "")
	if _, ok := noJWT["assets"]; ok {
		t.Error("assets metadata must be absent without a completion JWT")
	}
	if hasAssetBinding(noJWT) {
		t.Error("assets binding must be absent without a completion JWT")
	}

	withJWT := metadataFromMultipart(t, upload, "completion-token")
	if _, ok := withJWT["assets"]; !ok {
		t.Error("assets metadata must be present with a completion JWT")
	}
	if !hasAssetBinding(withJWT) {
		t.Error("assets binding must be present with a completion JWT")
	}
}

func TestBuildAssetBatch_EncodesFilePartsPerHash(t *testing.T) {
	assets := map[string]StaticAsset{
		"hash-svg": {Path: "/next.svg", Content: []byte("<svg/>"), Hash: "hash-svg"},
	}
	body, contentType, err := buildAssetBatch([]string{"hash-svg"}, assets)
	if err != nil {
		t.Fatalf("buildAssetBatch: %v", err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	part, err := mr.NextPart()
	if err != nil {
		t.Fatalf("read part: %v", err)
	}
	if part.FormName() != "hash-svg" || part.FileName() != "hash-svg" {
		t.Errorf("part name/filename = %q/%q, want the content hash for both", part.FormName(), part.FileName())
	}
	if ct := part.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("part Content-Type = %q, want image/svg+xml", ct)
	}
	data, _ := io.ReadAll(part)
	if string(data) != base64.StdEncoding.EncodeToString([]byte("<svg/>")) {
		t.Errorf("part body must be the base64-encoded contents, got %q", data)
	}
}

func TestDeployNextWorker_MissingAccountID_Errors(t *testing.T) {
	t.Setenv(envCloudflareAccountID, "")
	fake := &fakeCloudflare{}
	manifest := &deploymentsv1.Manifest{
		Functions: []*deploymentsv1.ManifestFunction{
			{LogicalName: "index", Framework: "next", RouteId: "/"},
		},
	}

	_, err := deployNextWorker(context.Background(), Config{Cloudflare: fake}, manifest, nil, nil)
	if err == nil {
		t.Fatal("expected an error when CLOUDFLARE_ACCOUNT_ID is unset")
	}
}
