package devserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	runtimev1 "github.com/ocelhq/ocel/pkg/proto/runtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/runtime/v1/runtimev1connect"
)

// TestRuntimePresignUpload_ForwardsToOcelAPI proves the dev RuntimeService
// mounted on the Mux forwards PresignUpload to the Ocel API presign endpoint
// (with the leader's token + projectID) and returns the API's response
// verbatim to the SDK - the CLI owns no cloud mechanics itself.
func TestRuntimePresignUpload_ForwardsToOcelAPI(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody presignRequestBody

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(presignResponseBody{
			SessionID: "sess_123",
			Files: []presignedTarget{
				{URL: "http://minio.local/put", Key: "org/proj/user/a.png", Name: "a.png", ContentDisposition: "inline"},
			},
		})
	}))
	defer api.Close()

	s := New(api.URL, "leader-tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := runtimev1connect.NewRuntimeServiceClient(http.DefaultClient, ts.URL)
	resp, err := client.PresignUpload(context.Background(), &runtimev1.PresignUploadRequest{
		Bucket: "storage",
		Files: []*runtimev1.PresignFile{
			{Key: "a.png", Name: "a.png", Size: 2048, MimeType: "image/png"},
		},
		Metadata:           []byte(`{"uploader":"avatar"}`),
		ContentDisposition: "inline",
		CallbackBaseUrl:    "http://localhost:3000/api/upload",
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	if gotPath != "/api/blob/presign" {
		t.Fatalf("forwarded path = %q, want /api/blob/presign", gotPath)
	}
	if gotAuth != "Bearer leader-tok" {
		t.Fatalf("forwarded auth = %q, want Bearer leader-tok", gotAuth)
	}
	if gotBody.ProjectID != "proj_1" {
		t.Fatalf("forwarded projectId = %q, want proj_1", gotBody.ProjectID)
	}
	if gotBody.Bucket != "storage" {
		t.Fatalf("forwarded bucket = %q, want storage", gotBody.Bucket)
	}
	if len(gotBody.Files) != 1 || gotBody.Files[0].Key != "a.png" || gotBody.Files[0].Size != 2048 {
		t.Fatalf("forwarded files = %+v, want one a.png/2048", gotBody.Files)
	}
	if string(gotBody.Metadata) != `{"uploader":"avatar"}` {
		t.Fatalf("forwarded metadata = %q, want verbatim envelope", gotBody.Metadata)
	}
	if gotBody.CallbackBaseURL != "http://localhost:3000/api/upload" {
		t.Fatalf("forwarded callbackBaseUrl = %q", gotBody.CallbackBaseURL)
	}

	if resp.GetSessionId() != "sess_123" {
		t.Fatalf("sessionId = %q, want sess_123", resp.GetSessionId())
	}
	if len(resp.GetFiles()) != 1 || resp.GetFiles()[0].GetUrl() != "http://minio.local/put" || resp.GetFiles()[0].GetKey() != "org/proj/user/a.png" {
		t.Fatalf("targets = %+v, want the API response verbatim", resp.GetFiles())
	}
	if resp.GetFiles()[0].GetContentDisposition() != "inline" {
		t.Fatalf("target contentDisposition = %q, want inline", resp.GetFiles()[0].GetContentDisposition())
	}
}

// TestRuntimePresignUpload_PropagatesAPIError surfaces a non-200 from the Ocel
// API as a Connect error rather than a bogus empty success.
func TestRuntimePresignUpload_PropagatesAPIError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer api.Close()

	s := New(api.URL, "tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := runtimev1connect.NewRuntimeServiceClient(http.DefaultClient, ts.URL)
	_, err := client.PresignUpload(context.Background(), &runtimev1.PresignUploadRequest{
		Bucket: "storage",
		Files:  []*runtimev1.PresignFile{{Key: "a.png", Name: "a.png", Size: 1, MimeType: "image/png"}},
	})
	if err == nil {
		t.Fatal("PresignUpload: expected error on API 401, got nil")
	}
}

// TestRuntimeVerifyUploadSignature_ForwardsToOcelAPI proves the shim forwards
// VerifyUploadSignature to POST /api/blob/verify (with the leader token) and
// returns the API's verdict + verbatim metadata bytes to the route.
func TestRuntimeVerifyUploadSignature_ForwardsToOcelAPI(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody signedCompletion
	rawMetadata := []byte(`{"uploader":"avatar","metadata":{"userId":"u1"}}`)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		// The API renders the stored metadata (base64) into the JSON []byte
		// field; encoding/json base64-encodes rawMetadata for us here.
		json.NewEncoder(w).Encode(verifyResponseBody{Valid: true, Metadata: rawMetadata})
	}))
	defer api.Close()

	s := New(api.URL, "leader-tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := runtimev1connect.NewRuntimeServiceClient(http.DefaultClient, ts.URL)
	resp, err := client.VerifyUploadSignature(context.Background(), &runtimev1.VerifyUploadSignatureRequest{
		SessionId: "sess_1",
		Signature: "sig",
		File:      &runtimev1.CompletedFile{Key: "org/proj/user/a.png", Name: "a.png", Size: 3, MimeType: "image/png"},
	})
	if err != nil {
		t.Fatalf("VerifyUploadSignature: %v", err)
	}
	if gotPath != "/api/blob/verify" || gotAuth != "Bearer leader-tok" {
		t.Fatalf("forwarded path/auth = %q/%q", gotPath, gotAuth)
	}
	if gotBody.SessionID != "sess_1" || gotBody.Signature != "sig" || gotBody.File.Key != "org/proj/user/a.png" {
		t.Fatalf("forwarded body = %+v", gotBody)
	}
	if !resp.GetValid() || string(resp.GetMetadata()) != string(rawMetadata) {
		t.Fatalf("resp = valid:%v metadata:%q, want valid + verbatim bytes", resp.GetValid(), resp.GetMetadata())
	}
}

// TestRuntimeGetUploadStatus_ForwardsToOcelAPI proves the shim forwards
// GetUploadStatus to GET /api/blob/status?sessionId=... and maps the API's
// string state onto the proto enum.
func TestRuntimeGetUploadStatus_ForwardsToOcelAPI(t *testing.T) {
	var gotPath, gotQuery string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("sessionId")
		json.NewEncoder(w).Encode(statusResponseBody{State: "succeeded"})
	}))
	defer api.Close()

	s := New(api.URL, "tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := runtimev1connect.NewRuntimeServiceClient(http.DefaultClient, ts.URL)
	resp, err := client.GetUploadStatus(context.Background(), &runtimev1.GetUploadStatusRequest{SessionId: "sess_9"})
	if err != nil {
		t.Fatalf("GetUploadStatus: %v", err)
	}
	if gotPath != "/api/blob/status" || gotQuery != "sess_9" {
		t.Fatalf("forwarded path/query = %q/%q", gotPath, gotQuery)
	}
	if resp.GetState() != runtimev1.UploadState_UPLOAD_STATE_SUCCEEDED {
		t.Fatalf("state = %v, want SUCCEEDED", resp.GetState())
	}
}

// TestRuntimeVerifyUploadSignature_PropagatesAPIError surfaces a non-200 as a
// Connect error rather than a bogus verdict.
func TestRuntimeVerifyUploadSignature_PropagatesAPIError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer api.Close()

	s := New(api.URL, "tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	defer ts.Close()

	client := runtimev1connect.NewRuntimeServiceClient(http.DefaultClient, ts.URL)
	_, err := client.VerifyUploadSignature(context.Background(), &runtimev1.VerifyUploadSignatureRequest{
		SessionId: "s", File: &runtimev1.CompletedFile{Key: "k"},
	})
	if err == nil {
		t.Fatal("expected error on API 500, got nil")
	}
	_ = connect.CodeOf(err)
}
