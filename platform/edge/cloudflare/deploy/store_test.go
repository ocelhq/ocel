package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	storeBootstrapCred = "bootstrap-cred"
	storeOwnerToken    = "owner-token"
)

func fakeStoreServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	var (
		staged  []edge.DeploymentRecord
		history []edge.HistoryEntry
		version *string
	)
	owner, live := storeOwnerToken, secret
	if secret == "" {
		owner = ""
	}
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if live == "" || r.Header.Get("Authorization") != "Bearer "+live {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /{slug}/initialize", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+storeBootstrapCred {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			OwnerToken string `json:"ownerToken"`
			Secret     string `json:"secret"`
			Force      bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OwnerToken == "" || body.Secret == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if owner == "" || body.Force {
			owner, live = body.OwnerToken, body.Secret
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"ownerToken": owner, "secret": live})
	})
	mux.HandleFunc("PUT /{slug}/staged", authed(func(w http.ResponseWriter, r *http.Request) {
		var rec edge.DeploymentRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		staged = append(staged, rec)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/promote", authed(func(w http.ResponseWriter, r *http.Request) {
		var p edge.Promotion
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		history = append([]edge.HistoryEntry{{Promotion: p, Active: true}}, history...)
		for i := range history[1:] {
			history[i+1].Active = false
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /{slug}/history", authed(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(history)
	}))
	mux.HandleFunc("POST /{slug}/prune", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			KeepN int `json:"keepN"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := edge.PruneResult{}
		for i, h := range history {
			if i < body.KeepN || h.Active {
				result.KeptPromotionIDs = append(result.KeptPromotionIDs, h.PromotionID)
			} else {
				result.RemovedPromotionIDs = append(result.RemovedPromotionIDs, h.PromotionID)
			}
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	mux.HandleFunc("GET /{slug}/version-stamp", authed(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]*string{"version": version})
	}))
	mux.HandleFunc("PUT /{slug}/version-stamp", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		version = &body.Version
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/destroy", authed(func(w http.ResponseWriter, _ *http.Request) {
		staged, history, version = nil, nil, nil
		owner, live = "", ""
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testState(endpoint, secret string) edge.RootStackState {
	return edge.RootStackState{
		edge.RootStackKeySlug:       "acme-web",
		edge.RootStackKeyEndpoint:   endpoint,
		edge.RootStackKeySecret:     secret,
		edge.RootStackKeyOwnerToken: storeOwnerToken,
	}
}

func TestPutStaged(t *testing.T) {
	t.Parallel()

	t.Run("a deployment record round trips", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		record := edge.DeploymentRecord{
			App: "web", Identity: "b1", FunctionURLs: map[string]string{"/": "https://fn"},
			AssetPrefix: "b1", IsrPrefix: "prod/proj/web/b1", CreatedAt: 100,
		}
		if err := (&provider{}).PutStaged(t.Context(), testState(srv.URL, "s3cr3t"), record); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
	})

	t.Run("the wrong write secret is rejected", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		err := (&provider{}).PutStaged(t.Context(), testState(srv.URL, "wrong"), edge.DeploymentRecord{App: "web", Identity: "b1"})
		if err == nil {
			t.Fatal("expected an error for the wrong write secret")
		}
	})
}

func TestPromotionHistory(t *testing.T) {
	t.Parallel()

	t.Run("the newest promotion is the active one", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		state := testState(srv.URL, "s3cr3t")
		promotion := edge.Promotion{PromotionID: "promo-1", Ts: 1000, Builds: map[string]string{"web": "b1"}}

		if err := p.Promote(t.Context(), state, promotion, ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		history, err := p.History(t.Context(), state, "")
		if err != nil {
			t.Fatalf("History: %v", err)
		}
		if len(history) != 1 {
			t.Fatalf("history = %v, want 1 entry", history)
		}
		if history[0].PromotionID != "promo-1" || !history[0].Active {
			t.Errorf("history[0] = %+v, want promo-1 active", history[0])
		}
	})

	t.Run("pruning keeps the window and pins the active promotion", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		state := testState(srv.URL, "s3cr3t")

		for _, id := range []string{"p1", "p2", "p3"} {
			if err := p.Promote(t.Context(), state, edge.Promotion{PromotionID: id, Ts: 1, Builds: map[string]string{"web": id}}, ""); err != nil {
				t.Fatalf("Promote(%s): %v", id, err)
			}
		}

		result, err := p.DeletePromotionArtifacts(t.Context(), state, 1, "")
		if err != nil {
			t.Fatalf("DeletePromotionArtifacts: %v", err)
		}
		want := []string{"p2", "p1"}
		if len(result.RemovedPromotionIDs) != len(want) || result.RemovedPromotionIDs[0] != want[0] || result.RemovedPromotionIDs[1] != want[1] {
			t.Errorf("RemovedPromotionIDs = %v, want %v", result.RemovedPromotionIDs, want)
		}
	})
}

func TestStorePointer(t *testing.T) {
	t.Parallel()

	t.Run("promote, history and prune carry the pointer only when there is one", func(t *testing.T) {
		t.Parallel()

		var (
			promoteBodies []map[string]any
			historyQuery  []string
			pruneBodies   []map[string]any
		)
		mux := http.NewServeMux()
		mux.HandleFunc("POST /{slug}/promote", func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			promoteBodies = append(promoteBodies, b)
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("GET /{slug}/history", func(w http.ResponseWriter, r *http.Request) {
			historyQuery = append(historyQuery, r.URL.Query().Get("pointer"))
			_ = json.NewEncoder(w).Encode([]edge.HistoryEntry{})
		})
		mux.HandleFunc("POST /{slug}/prune", func(w http.ResponseWriter, r *http.Request) {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			pruneBodies = append(pruneBodies, b)
			_ = json.NewEncoder(w).Encode(edge.PruneResult{})
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		p := &provider{}
		state := testState(srv.URL, "s3cr3t")
		ctx := t.Context()

		if err := p.Promote(ctx, state, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"}}, "pr-42"); err != nil {
			t.Fatalf("Promote(preview): %v", err)
		}
		if _, err := p.History(ctx, state, "pr-42"); err != nil {
			t.Fatalf("History(preview): %v", err)
		}
		if _, err := p.DeletePromotionArtifacts(ctx, state, 3, "pr-42"); err != nil {
			t.Fatalf("Prune(preview): %v", err)
		}
		if err := p.Promote(ctx, state, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": "b2"}}, ""); err != nil {
			t.Fatalf("Promote(prod): %v", err)
		}
		if _, err := p.History(ctx, state, ""); err != nil {
			t.Fatalf("History(prod): %v", err)
		}
		if _, err := p.DeletePromotionArtifacts(ctx, state, 3, ""); err != nil {
			t.Fatalf("Prune(prod): %v", err)
		}

		if promoteBodies[0]["pointer"] != "pr-42" || promoteBodies[0]["promotionId"] != "p1" {
			t.Errorf("preview promote body = %v, want pointer pr-42 alongside the promotion", promoteBodies[0])
		}
		if _, ok := promoteBodies[1]["pointer"]; ok {
			t.Errorf("production promote body carried a pointer field: %v", promoteBodies[1])
		}
		if historyQuery[0] != "pr-42" || historyQuery[1] != "" {
			t.Errorf("history pointer queries = %v, want [pr-42 <empty>]", historyQuery)
		}
		if pruneBodies[0]["pointer"] != "pr-42" {
			t.Errorf("preview prune body = %v, want pointer pr-42", pruneBodies[0])
		}
		if _, ok := pruneBodies[1]["pointer"]; ok {
			t.Errorf("production prune body carried a pointer field: %v", pruneBodies[1])
		}
	})

	t.Run("removing a pointer sends it and returns what there is to reclaim", func(t *testing.T) {
		t.Parallel()

		var body map[string]any
		mux := http.NewServeMux()
		mux.HandleFunc("POST /{slug}/remove-pointer", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(edge.PruneResult{
				RemovedPromotionIDs: []string{"promo-1"},
				RemovedRecordKeys:   []string{"record:web/b1"},
			})
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		result, err := (&provider{}).RemovePointer(t.Context(), testState(srv.URL, "s3cr3t"), "pr-42")
		if err != nil {
			t.Fatalf("RemovePointer: %v", err)
		}
		if body["pointer"] != "pr-42" {
			t.Errorf("remove-pointer body = %v, want the pointer pr-42", body)
		}
		if len(result.RemovedRecordKeys) != 1 || result.RemovedRecordKeys[0] != "record:web/b1" {
			t.Errorf("result = %+v, want the removed record keys to reclaim", result)
		}
	})
}

func TestStoreRequest(t *testing.T) {
	t.Parallel()

	t.Run("a state carrying no endpoint is an error", func(t *testing.T) {
		t.Parallel()

		err := (&provider{}).PutStaged(t.Context(), edge.RootStackState{}, edge.DeploymentRecord{App: "web", Identity: "b1"})
		if err == nil {
			t.Fatal("expected an error when the root-stack state carries no endpoint")
		}
	})

	t.Run("an unavailable store is retried until it answers", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		mux := http.NewServeMux()
		mux.HandleFunc("PUT /{slug}/staged", func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts < 3 {
				w.Header().Set("Retry-After-Ms", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		if err := (&provider{}).PutStaged(t.Context(), testState(srv.URL, "s3cr3t"), edge.DeploymentRecord{App: "web"}); err != nil {
			t.Fatalf("PutStaged: %v", err)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3: the two unavailable answers must have been retried", attempts)
		}
	})

	t.Run("a rejected credential is not retried", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)

		if err := (&provider{}).PutStaged(t.Context(), testState(srv.URL, "wrong"), edge.DeploymentRecord{App: "web"}); err == nil {
			t.Fatal("PutStaged err = nil, want the rejection surfaced")
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1", attempts)
		}
	})

	t.Run("a cancelled context stops the retry without waiting out the backoff", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		ctx, cancel := context.WithCancel(t.Context())
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		if err := (&provider{}).PutStaged(ctx, testState(srv.URL, "s3cr3t"), edge.DeploymentRecord{App: "web"}); err == nil {
			t.Fatal("PutStaged err = nil, want the failure surfaced")
		}
		if attempts != 1 {
			t.Errorf("attempts = %d, want 1: a cancelled context must not wait out the backoff", attempts)
		}
	})
}

func TestVersionStamp(t *testing.T) {
	t.Parallel()

	t.Run("an unset stamp reads empty", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		v, _, err := (&provider{}).getVersionStamp(t.Context(), srv.URL, "acme-web", "s3cr3t")
		if err != nil {
			t.Fatalf("getVersionStamp: %v", err)
		}
		if v != "" {
			t.Errorf("version = %q, want empty", v)
		}
	})

	t.Run("a written stamp reads back", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		if err := p.putVersionStamp(t.Context(), srv.URL, "acme-web", "s3cr3t", "v2"); err != nil {
			t.Fatalf("putVersionStamp: %v", err)
		}
		v, _, err := p.getVersionStamp(t.Context(), srv.URL, "acme-web", "s3cr3t")
		if err != nil {
			t.Fatalf("getVersionStamp: %v", err)
		}
		if v != "v2" {
			t.Errorf("version = %q, want v2", v)
		}
	})
}

func TestDestroyInstance(t *testing.T) {
	t.Parallel()

	t.Run("a state carrying no secret is a no-op", func(t *testing.T) {
		t.Parallel()

		if err := (&provider{}).DestroyInstance(t.Context(), edge.RootStackState{}); err != nil {
			t.Fatalf("DestroyInstance(empty) err = %v, want nil", err)
		}
	})

	t.Run("the instance is wiped and stops honouring its secret", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		state := testState(srv.URL, "s3cr3t")
		if err := p.Promote(t.Context(), state, edge.Promotion{PromotionID: "p1", Ts: 1, Builds: map[string]string{"web": "b1"}}, ""); err != nil {
			t.Fatalf("Promote: %v", err)
		}
		if err := p.DestroyInstance(t.Context(), state); err != nil {
			t.Fatalf("DestroyInstance: %v", err)
		}
		if _, err := p.History(t.Context(), state, ""); err == nil {
			t.Error("history after destroy: err = nil, want the wiped instance to reject the secret")
		}
	})

	t.Run("an already-wiped instance is a success", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &provider{}
		state := testState(srv.URL, "s3cr3t")
		if err := p.DestroyInstance(t.Context(), state); err != nil {
			t.Fatalf("DestroyInstance: %v", err)
		}
		if err := p.DestroyInstance(t.Context(), state); err != nil {
			t.Fatalf("DestroyInstance on an already-wiped instance: err = %v, want nil", err)
		}
	})
}
