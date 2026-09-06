package devblob

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
)

type app struct {
	server    *httptest.Server
	delivered []callback
	answer    int
}

func newApp(t *testing.T) *app {
	t.Helper()
	a := &app{answer: http.StatusOK}
	a.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("op") != "callback" {
			http.Error(w, "unknown op", http.StatusBadRequest)
			return
		}
		var body callback
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		a.delivered = append(a.delivered, body)
		w.WriteHeader(a.answer)
	}))
	t.Cleanup(a.server.Close)
	return a
}

func store(t *testing.T) (*Store, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	s := New(dir, "http://127.0.0.1:0")
	mux := http.NewServeMux()
	s.Routes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	s.baseURL = server.URL
	return s, server, dir
}

func presign(t *testing.T, s *Store, callbackBaseURL, key string) *blobv1.PresignUploadResponse {
	t.Helper()
	res, err := s.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
		Bucket:          "uploads",
		Metadata:        []byte(`{"uploader":"document"}`),
		CallbackBaseUrl: callbackBaseURL,
		Files: []*blobv1.PresignFile{
			{Key: key, Name: "report.pdf", Size: 13, MimeType: "application/pdf"},
		},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	return res
}

func put(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestStore(t *testing.T) {
	t.Run("an upload lands on disk under the key the app asked for, and the callback carries it", func(t *testing.T) {
		receiver := newApp(t)
		s, _, dir := store(t)

		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		if got := s.mustStatus(t, session.GetSessionId()); got != blobv1.UploadState_UPLOAD_STATE_PENDING {
			t.Fatalf("state before the PUT = %v, want pending", got)
		}

		if res := put(t, session.GetFiles()[0].GetUrl(), "journey-bytes"); res.StatusCode != http.StatusOK {
			t.Fatalf("PUT answered %d, want 200", res.StatusCode)
		}

		stored, err := os.ReadFile(filepath.Join(dir, "documents", "report.pdf"))
		if err != nil {
			t.Fatalf("read the stored object: %v", err)
		}
		if string(stored) != "journey-bytes" {
			t.Errorf("stored bytes = %q, want the body of the PUT", stored)
		}
		if got := s.mustStatus(t, session.GetSessionId()); got != blobv1.UploadState_UPLOAD_STATE_SUCCEEDED {
			t.Errorf("state after the PUT = %v, want succeeded", got)
		}

		if len(receiver.delivered) != 1 {
			t.Fatalf("the app received %d callbacks, want 1", len(receiver.delivered))
		}
		done := receiver.delivered[0]
		verified, err := s.VerifyUploadSignature(context.Background(), &blobv1.VerifyUploadSignatureRequest{
			SessionId: done.SessionID,
			Signature: done.Signature,
			File: &blobv1.CompletedFile{
				Key: done.File.Key, Name: done.File.Name, Size: done.File.Size, MimeType: done.File.MimeType,
			},
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if !verified.GetValid() {
			t.Fatal("the store refused the signature it wrote itself")
		}
		if string(verified.GetMetadata()) != `{"uploader":"document"}` {
			t.Errorf("metadata = %q, want the metadata the presign carried", verified.GetMetadata())
		}
	})

	t.Run("a tampered signature is refused", func(t *testing.T) {
		receiver := newApp(t)
		s, _, _ := store(t)
		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		put(t, session.GetFiles()[0].GetUrl(), "journey-bytes")

		done := receiver.delivered[0]
		verified, err := s.VerifyUploadSignature(context.Background(), &blobv1.VerifyUploadSignatureRequest{
			SessionId: done.SessionID,
			Signature: done.Signature,
			File: &blobv1.CompletedFile{
				Key: done.File.Key, Name: "other.pdf", Size: done.File.Size, MimeType: done.File.MimeType,
			},
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if verified.GetValid() {
			t.Fatal("the store accepted a completion for a file it never signed")
		}
	})

	t.Run("a PUT with no ticket stores nothing", func(t *testing.T) {
		_, server, dir := store(t)

		res := put(t, server.URL+UploadPath+"?ticket=guessed", "smuggled")
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("PUT with an unknown ticket answered %d, want 403", res.StatusCode)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read the store: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("the store holds %d entries after a ticketless PUT, want none", len(entries))
		}
	})

	t.Run("a key that climbs out of the store is refused at presign", func(t *testing.T) {
		s, _, _ := store(t)
		if _, err := s.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{
			Bucket: "uploads",
			Files:  []*blobv1.PresignFile{{Key: "../escape.pdf", Name: "escape.pdf", Size: 1, MimeType: "application/pdf"}},
		}); err == nil {
			t.Fatal("the store presigned a key that leaves its directory")
		}
	})

	t.Run("the ticket is spent by the upload it signed, so a replay stores nothing", func(t *testing.T) {
		receiver := newApp(t)
		s, _, dir := store(t)

		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		url := session.GetFiles()[0].GetUrl()
		if res := put(t, url, "journey-bytes"); res.StatusCode != http.StatusOK {
			t.Fatalf("first PUT answered %d, want 200", res.StatusCode)
		}

		if res := put(t, url, "replayed-bytes"); res.StatusCode != http.StatusForbidden {
			t.Fatalf("replayed PUT answered %d, want 403", res.StatusCode)
		}
		stored, err := os.ReadFile(filepath.Join(dir, "documents", "report.pdf"))
		if err != nil {
			t.Fatalf("read the stored object: %v", err)
		}
		if string(stored) != "journey-bytes" {
			t.Errorf("stored bytes = %q, want the body of the first PUT", stored)
		}
		if len(receiver.delivered) != 1 {
			t.Errorf("the app received %d callbacks, want 1", len(receiver.delivered))
		}
	})

	t.Run("a ticket the app never spent is refused once its session has expired", func(t *testing.T) {
		receiver := newApp(t)
		s, _, dir := store(t)
		moment := time.Now()
		s.now = func() time.Time { return moment }

		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		moment = moment.Add(sessionTTL + time.Minute)

		if res := put(t, session.GetFiles()[0].GetUrl(), "journey-bytes"); res.StatusCode != http.StatusForbidden {
			t.Fatalf("PUT on an expired ticket answered %d, want 403", res.StatusCode)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("the store holds %v (err %v) after an expired PUT, want nothing", entries, err)
		}
		if got := s.mustStatus(t, session.GetSessionId()); got != blobv1.UploadState_UPLOAD_STATE_EXPIRED {
			t.Errorf("state = %v, want expired", got)
		}
	})

	t.Run("a completion signed before the session expired is refused after it", func(t *testing.T) {
		receiver := newApp(t)
		s, _, _ := store(t)
		moment := time.Now()
		s.now = func() time.Time { return moment }

		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		put(t, session.GetFiles()[0].GetUrl(), "journey-bytes")
		done := receiver.delivered[0]
		moment = moment.Add(sessionTTL + time.Minute)

		verified, err := s.VerifyUploadSignature(context.Background(), &blobv1.VerifyUploadSignatureRequest{
			SessionId: done.SessionID,
			Signature: done.Signature,
			File: &blobv1.CompletedFile{
				Key: done.File.Key, Name: done.File.Name, Size: done.File.Size, MimeType: done.File.MimeType,
			},
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if verified.GetValid() {
			t.Fatal("the store verified a completion for a session that has expired")
		}
	})

	t.Run("a body larger than the presigned size is refused and leaves no object behind", func(t *testing.T) {
		receiver := newApp(t)
		s, _, dir := store(t)

		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		res := put(t, session.GetFiles()[0].GetUrl(), strings.Repeat("x", 64))
		if res.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("PUT of 64 bytes against a 13-byte ticket answered %d, want 413", res.StatusCode)
		}
		entries, err := os.ReadDir(filepath.Join(dir, "documents"))
		if err != nil {
			t.Fatalf("read the store: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("the store holds %d entries after an oversized PUT, want none", len(entries))
		}
		if len(receiver.delivered) != 0 {
			t.Errorf("the app received %d callbacks for an upload the store refused, want none", len(receiver.delivered))
		}
	})

	t.Run("an upload the app refuses stays pending", func(t *testing.T) {
		receiver := newApp(t)
		receiver.answer = http.StatusInternalServerError
		s, _, _ := store(t)

		session := presign(t, s, receiver.server.URL, "documents/report.pdf")
		if res := put(t, session.GetFiles()[0].GetUrl(), "journey-bytes"); res.StatusCode != http.StatusBadGateway {
			t.Fatalf("PUT answered %d, want 502 when the app refuses the callback", res.StatusCode)
		}
		if got := s.mustStatus(t, session.GetSessionId()); got != blobv1.UploadState_UPLOAD_STATE_PENDING {
			t.Errorf("state = %v, want pending until the app has taken the completion", got)
		}
	})
}

func (s *Store) mustStatus(t *testing.T, sessionID string) blobv1.UploadState {
	t.Helper()
	res, err := s.GetUploadStatus(context.Background(), &blobv1.GetUploadStatusRequest{SessionId: sessionID})
	if err != nil {
		t.Fatalf("GetUploadStatus: %v", err)
	}
	return res.GetState()
}
