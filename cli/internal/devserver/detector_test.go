package devserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// TestDetectorSweep_DeliversCallbacks proves one sweep asks the API to detect
// this project's landings (forwarding the leader token) and POSTs each
// resulting completion to its callbackBaseUrl as op=callback with the signed
// body verbatim - the CLI half of the completion architecture.
func TestDetectorSweep_DeliversCallbacks(t *testing.T) {
	var gotOp string
	var gotCallback signedCompletion
	callbackHit := make(chan struct{}, 1)

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOp = r.URL.Query().Get("op")
		_ = json.NewDecoder(r.Body).Decode(&gotCallback)
		w.WriteHeader(http.StatusOK)
		callbackHit <- struct{}{}
	}))
	defer app.Close()

	var gotAuth string
	var gotDetect detectRequestBody
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotDetect)
		json.NewEncoder(w).Encode(detectResponseBody{Completions: []completion{{
			CallbackBaseURL: app.URL,
			SessionID:       "sess_1",
			File:            completedFile{Key: "org/proj/user/a.png", Name: "a.png", Size: 3, MimeType: "image/png"},
			Signature:       "sig-abc",
		}}})
	}))
	defer api.Close()

	d := newDetector(api.URL, "leader-tok", "proj_1")
	if err := d.sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	<-callbackHit
	if gotAuth != "Bearer leader-tok" || gotDetect.ProjectID != "proj_1" {
		t.Fatalf("detect auth/projectId = %q/%q", gotAuth, gotDetect.ProjectID)
	}
	if gotOp != "callback" {
		t.Fatalf("callback op = %q, want callback", gotOp)
	}
	if gotCallback.SessionID != "sess_1" || gotCallback.Signature != "sig-abc" || gotCallback.File.Key != "org/proj/user/a.png" {
		t.Fatalf("callback body = %+v", gotCallback)
	}
}

// TestDetectorSweep_NoCompletionsNoCallback proves an empty detect result
// (nothing landed) fires no callback - the common idle tick.
func TestDetectorSweep_NoCompletionsNoCallback(t *testing.T) {
	var appHits int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&appHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(detectResponseBody{})
	}))
	defer api.Close()

	d := newDetector(api.URL, "tok", "proj_1")
	if err := d.sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := atomic.LoadInt32(&appHits); got != 0 {
		t.Fatalf("app callback hits = %d, want 0", got)
	}
}

// TestDetectorSweep_CallbackRejectionSurfaces proves a non-2xx from the app
// route surfaces as a sweep error (so the loop reports rather than dropping it
// silently).
func TestDetectorSweep_CallbackRejectionSurfaces(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	}))
	defer app.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(detectResponseBody{Completions: []completion{{
			CallbackBaseURL: app.URL, SessionID: "s", Signature: "sig",
			File: completedFile{Key: "k"},
		}}})
	}))
	defer api.Close()

	d := newDetector(api.URL, "tok", "proj_1")
	if err := d.sweep(context.Background()); err == nil {
		t.Fatal("expected sweep error on app 401, got nil")
	}
}

// TestDetectorRun_StopsWithContext proves the loop's lifetime is bounded by
// ctx: cancelling ends run promptly.
func TestDetectorRun_StopsWithContext(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(detectResponseBody{})
	}))
	defer api.Close()

	d := newDetector(api.URL, "tok", "proj_1")
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.run(ctx, nil)
	}()
	cancel()
	wg.Wait()
}
