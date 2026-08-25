package devserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/console/blob"
	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/blob/v1/blobv1connect"
)

func serveBlob(t *testing.T, handler http.HandlerFunc) blobv1connect.BucketServiceClient {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	s := New(api.URL, "leader-tok", "proj_1", "http://127.0.0.1:0")
	ts := httptest.NewServer(s.Mux())
	t.Cleanup(ts.Close)

	return blobv1connect.NewBucketServiceClient(http.DefaultClient, ts.URL)
}

func TestPresignUpload(t *testing.T) {
	t.Parallel()

	t.Run("refuses a key that climbs out of the tenant prefix", func(t *testing.T) {
		t.Parallel()
		reached := false
		client := serveBlob(t, func(w http.ResponseWriter, r *http.Request) {
			reached = true
			json.NewEncoder(w).Encode(blob.PresignResponseBody{SessionID: "sess_123"})
		})

		_, err := client.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
			Bucket: "storage",
			Files: []*blobv1.PresignFile{
				{Key: "../../etc/passwd", Name: "passwd", Size: 1, MimeType: "text/plain"},
			},
		})

		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("PresignUpload err = %v, want %v", err, connect.CodeInvalidArgument)
		}
		if reached {
			t.Fatal("a traversal key was forwarded to the API")
		}
	})

	t.Run("forwards to the Ocel API", func(t *testing.T) {
		t.Parallel()
		var gotAuth, gotPath string
		var gotBody blob.PresignRequestBody

		client := serveBlob(t, func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(blob.PresignResponseBody{
				SessionID: "sess_123",
				Files: []blob.PresignedTarget{
					{URL: "http://minio.local/put", Key: "org/proj/user/a.png", Name: "a.png", ContentDisposition: "inline"},
				},
			})
		})

		resp, err := client.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
			Bucket: "storage",
			Files: []*blobv1.PresignFile{
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
	})

	t.Run("propagates an API error", func(t *testing.T) {
		t.Parallel()
		client := serveBlob(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})

		_, err := client.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
			Bucket: "storage",
			Files:  []*blobv1.PresignFile{{Key: "a.png", Name: "a.png", Size: 1, MimeType: "image/png"}},
		})
		if err == nil {
			t.Fatal("PresignUpload: expected error on API 401, got nil")
		}
	})
}

func TestVerifyUploadSignature(t *testing.T) {
	t.Parallel()

	t.Run("forwards to the Ocel API", func(t *testing.T) {
		t.Parallel()
		var gotAuth, gotPath string
		var gotBody blob.SignedCompletion
		rawMetadata := []byte(`{"uploader":"avatar","metadata":{"userId":"u1"}}`)

		client := serveBlob(t, func(w http.ResponseWriter, r *http.Request) {
			gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			json.NewEncoder(w).Encode(blob.VerifyResponseBody{Valid: true, Metadata: rawMetadata})
		})

		resp, err := client.VerifyUploadSignature(context.Background(), &blobv1.VerifyUploadSignatureRequest{
			SessionId: "sess_1",
			Signature: "sig",
			File:      &blobv1.CompletedFile{Key: "org/proj/user/a.png", Name: "a.png", Size: 3, MimeType: "image/png"},
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
	})

	t.Run("propagates an API error", func(t *testing.T) {
		t.Parallel()
		client := serveBlob(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})

		_, err := client.VerifyUploadSignature(context.Background(), &blobv1.VerifyUploadSignatureRequest{
			SessionId: "s", File: &blobv1.CompletedFile{Key: "k"},
		})
		if err == nil {
			t.Fatal("expected error on API 500, got nil")
		}
		if code := connect.CodeOf(err); code == 0 {
			t.Errorf("connect.CodeOf(err) = %v, want a non-OK code", code)
		}
	})
}

func TestGetUploadStatus(t *testing.T) {
	t.Parallel()

	t.Run("forwards to the Ocel API", func(t *testing.T) {
		t.Parallel()
		var gotPath, gotQuery string
		client := serveBlob(t, func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("sessionId")
			json.NewEncoder(w).Encode(blob.StatusResponseBody{State: "succeeded"})
		})

		resp, err := client.GetUploadStatus(context.Background(), &blobv1.GetUploadStatusRequest{SessionId: "sess_9"})
		if err != nil {
			t.Fatalf("GetUploadStatus: %v", err)
		}
		if gotPath != "/api/blob/status" || gotQuery != "sess_9" {
			t.Fatalf("forwarded path/query = %q/%q", gotPath, gotQuery)
		}
		if resp.GetState() != blobv1.UploadState_UPLOAD_STATE_SUCCEEDED {
			t.Fatalf("state = %v, want SUCCEEDED", resp.GetState())
		}
	})
}
