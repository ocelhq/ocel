package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

// metadataFromMultipart builds the worker upload body and parses its "metadata"
// part back into a map for assertion.
func metadataFromMultipart(t *testing.T, worker edge.Worker, assetsJWT string) map[string]any {
	t.Helper()
	body, contentType, err := buildScriptMultipart(worker, assetsJWT)
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

func TestBuildScriptMultipartEnablesObservability(t *testing.T) {
	worker := edge.Worker{
		Main: edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
	}

	meta := metadataFromMultipart(t, worker, "")
	obs, ok := meta["observability"].(map[string]any)
	if !ok {
		t.Fatalf("metadata has no observability object: %v", meta["observability"])
	}
	if obs["enabled"] != true {
		t.Errorf("observability.enabled = %v, want true", obs["enabled"])
	}
	logs, ok := obs["logs"].(map[string]any)
	if !ok || logs["enabled"] != true {
		t.Errorf("observability.logs not enabled: %v", obs["logs"])
	}
	traces, ok := obs["traces"].(map[string]any)
	if !ok || traces["enabled"] != true {
		t.Errorf("observability.traces not enabled: %v", obs["traces"])
	}
}

func TestScriptBindings_EmitsSecretTextAndPlainText(t *testing.T) {
	worker := edge.Worker{
		Main:    edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Vars:    map[string]string{"OCEL_EDGE_ACCESS_KEY_ID": "AKIA", "OCEL_ISR_BUCKET": "assets"},
		Secrets: map[string]string{"OCEL_EDGE_SECRET_KEY": "shh"},
	}

	meta := metadataFromMultipart(t, worker, "")
	bindings, ok := meta["bindings"].([]any)
	if !ok {
		t.Fatalf("metadata has no bindings array: %v", meta["bindings"])
	}

	byName := map[string]map[string]any{}
	for _, b := range bindings {
		m := b.(map[string]any)
		byName[m["name"].(string)] = m
	}

	secret, ok := byName["OCEL_EDGE_SECRET_KEY"]
	if !ok {
		t.Fatal("missing the OCEL_EDGE_SECRET_KEY binding")
	}
	if secret["type"] != "secret_text" {
		t.Errorf("secret binding type = %v, want secret_text", secret["type"])
	}
	if secret["text"] != "shh" {
		t.Errorf("secret binding text = %v, want shh", secret["text"])
	}

	if akid := byName["OCEL_EDGE_ACCESS_KEY_ID"]; akid == nil || akid["type"] != "plain_text" {
		t.Errorf("OCEL_EDGE_ACCESS_KEY_ID should be a plain_text binding, got %v", akid)
	}
}

func TestBuildScriptMultipart_AssetsBindingGatedOnCompletionJWT(t *testing.T) {
	worker := edge.Worker{
		AssetBinding: "ASSETS",
		Main:         edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("x")},
		Vars:         map[string]string{"FUNCTION_URLS": "{}"},
		Assets:       []edge.StaticAsset{{Path: "/a.svg", Hash: "h", Size: 1, Content: []byte("a")}},
	}

	// Without a completion JWT (e.g. a redeploy where the session returned no
	// buckets and the token was lost), neither the assets binding nor the assets
	// metadata may appear — Cloudflare rejects a binding without assets.
	noJWT := metadataFromMultipart(t, worker, "")
	if _, ok := noJWT["assets"]; ok {
		t.Error("assets metadata must be absent without a completion JWT")
	}
	if hasAssetBinding(noJWT) {
		t.Error("assets binding must be absent without a completion JWT")
	}

	withJWT := metadataFromMultipart(t, worker, "completion-token")
	if _, ok := withJWT["assets"]; !ok {
		t.Error("assets metadata must be present with a completion JWT")
	}
	if !hasAssetBinding(withJWT) {
		t.Error("assets binding must be present with a completion JWT")
	}
}

func TestBuildScriptMultipart_UploadsMainAndSiblingModules(t *testing.T) {
	worker := edge.Worker{
		Main:    edge.WorkerModule{Name: "index.js", ContentType: "application/javascript+module", Content: []byte("export default {}")},
		Modules: []edge.WorkerModule{{Name: "routing-manifest.json", ContentType: "text/plain", Content: []byte(`{"buildId":"b"}`)}},
	}

	body, contentType, err := buildScriptMultipart(worker, "")
	if err != nil {
		t.Fatalf("buildScriptMultipart: %v", err)
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}

	byName := map[string]string{}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		data, _ := io.ReadAll(part)
		byName[part.FormName()] = string(data)
	}

	if got := byName["index.js"]; got != "export default {}" {
		t.Errorf("index.js part = %q", got)
	}
	if got := byName["routing-manifest.json"]; got != `{"buildId":"b"}` {
		t.Errorf("routing-manifest.json part = %q", got)
	}
}

func TestBuildAssetBatch_EncodesFilePartsPerHash(t *testing.T) {
	assets := map[string]edge.StaticAsset{
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

func TestZoneOwns(t *testing.T) {
	cases := []struct {
		hostname string
		zone     string
		want     bool
	}{
		{"app.acme.com", "acme.com", true},
		{"acme.com", "acme.com", true},
		{"app.acme.com", "app.acme.com", true},
		{"app.acme.com", "other.com", false},
		{"app.acme.com", "me.com", false},
		{"notacme.com", "acme.com", false},
		{"app.acme.com", "cme.com", false},
	}
	for _, tc := range cases {
		if got := zoneOwns(tc.hostname, tc.zone); got != tc.want {
			t.Errorf("zoneOwns(%q, %q) = %v, want %v", tc.hostname, tc.zone, got, tc.want)
		}
	}
}

func TestDeployApp_MissingAccountID_Errors(t *testing.T) {
	t.Setenv(envAccountID, "")

	_, err := New().DeployApp(context.Background(), edge.AppDeployment{Name: "ocel-proj-prod"})
	if err == nil {
		t.Fatalf("expected an error when %s is unset", envAccountID)
	}
}

func TestBootstrap_ReportsExternalTrust(t *testing.T) {
	out, err := New().Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if out.Trust != edge.TrustExternal {
		t.Errorf("Trust = %q, want %q", out.Trust, edge.TrustExternal)
	}
	if len(out.Offers) != 0 {
		t.Errorf("Offers = %v, want none: Cloudflare provisions nothing of its own", out.Offers)
	}
	if len(out.Values) != 0 {
		t.Errorf("Values = %v, want none: Cloudflare provisions nothing of its own", out.Values)
	}
}
