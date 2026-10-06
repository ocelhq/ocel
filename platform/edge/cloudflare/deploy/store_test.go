package cloudflare

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	storeBootstrapCredential = "bootstrap-cred"
	storeOwnerToken          = "owner-token"
)

type labelTarget struct{ pointer, app string }

type fakeStore struct {
	mu       sync.Mutex
	pointers map[string]string
	served   map[string]map[string]router.DeploymentRecord
	apps     map[string]bool
	labels   map[string]labelTarget
	moves    []pointerMoveBody
	version  *string
	owner    string
	live     string
}

func (f *fakeStore) serving(pointer, app string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pointer == "" {
		pointer = router.DefaultPointer
	}
	return f.served[pointer][app].Build
}

func (f *fakeStore) findServedBuild(label string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	at, found := f.labels[label]
	if !found {
		return ""
	}
	served := f.served[at.pointer]
	if at.app == "" && len(served) == 1 {
		for _, record := range served {
			return record.Build
		}
	}
	return served[at.app].Build
}

func (f *fakeStore) dropLabels(pointer string) {
	for label, at := range f.labels {
		if at.pointer == pointer {
			delete(f.labels, label)
		}
	}
}

func (f *fakeStore) moved() []pointerMoveBody {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.moves)
}

func fakeStoreServer(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	srv, _ := fakeStoreFor(t, secret)
	return srv
}

func fakeStoreFor(t *testing.T, secret string) (*httptest.Server, *fakeStore) {
	t.Helper()
	f := &fakeStore{
		pointers: map[string]string{},
		served:   map[string]map[string]router.DeploymentRecord{},
		apps:     map[string]bool{},
		labels:   map[string]labelTarget{},
		owner:    storeOwnerToken,
		live:     secret,
	}
	if secret == "" {
		f.owner = ""
	}
	named := func(pointer string) string {
		if pointer == "" {
			return router.DefaultPointer
		}
		return pointer
	}
	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.live == "" || r.Header.Get("Authorization") != "Bearer "+f.live {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /{slug}/initialize", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+storeBootstrapCredential {
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
		if f.owner != "" && !body.Force {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.owner, f.live = body.OwnerToken, body.Secret
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /{slug}/pointer", authed(func(w http.ResponseWriter, r *http.Request) {
		var served *string
		if id, found := f.pointers[named(r.URL.Query().Get("pointer"))]; found {
			served = &id
		}
		_ = json.NewEncoder(w).Encode(map[string]*string{"promotionId": served})
	}))
	mux.HandleFunc("POST /{slug}/move-pointer", authed(func(w http.ResponseWriter, r *http.Request) {
		var body pointerMoveBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PromotionID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pointer := named(body.Pointer)
		if f.pointers[pointer] != body.Replaces {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.moves = append(f.moves, body)
		f.served[pointer] = map[string]router.DeploymentRecord{}
		for _, record := range body.Records {
			f.served[pointer][record.App] = record
			f.apps[record.App] = true
		}
		f.dropLabels(pointer)
		for _, served := range body.Labels {
			f.labels[served.Label] = labelTarget{pointer: pointer, app: served.App}
		}
		f.pointers[pointer] = body.PromotionID
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/remove-pointer", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Pointer string `json:"pointer"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Pointer == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		delete(f.pointers, body.Pointer)
		delete(f.served, body.Pointer)
		f.dropLabels(body.Pointer)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /{slug}/apps", authed(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(slices.Sorted(maps.Keys(f.apps)))
	}))
	mux.HandleFunc("GET /{slug}/version-stamp", authed(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]*string{"version": f.version})
	}))
	mux.HandleFunc("PUT /{slug}/version-stamp", authed(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Version string `json:"version"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.version = &body.Version
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/destroy", authed(func(w http.ResponseWriter, _ *http.Request) {
		f.pointers = map[string]string{}
		f.served = map[string]map[string]router.DeploymentRecord{}
		f.apps = map[string]bool{}
		f.labels = map[string]labelTarget{}
		f.version = nil
		f.owner, f.live = "", ""
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func stackOn(p *cloudflare, state edge.StackState) *stack {
	if p.store == nil {
		p.store = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	}
	s := &stack{p: p, state: state}
	if err := state.Private.Into(&s.own); err != nil {
		panic(err)
	}
	return s
}

func testState(endpoint, secret string) edge.StackState {
	return edge.StackState{
		Slug:       "acme-web",
		Endpoint:   endpoint,
		Secret:     secret,
		OwnerToken: storeOwnerToken,
		Tier:       environment.TierProduction,
	}
}

func movePointer(t *testing.T, p *cloudflare, state edge.StackState, move router.PointerMove) {
	t.Helper()
	if err := (routerStack{s: stackOn(p, state)}).MovePointer(t.Context(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(%s): %v", move.Promotion.PromotionID, err)
	}
}

func pointerMoveOf(promotionID, pointer string, records ...router.DeploymentRecord) router.PointerMove {
	move := router.PointerMove{
		Pointer:   pointer,
		Promotion: router.Promotion{PromotionID: promotionID, Ts: 1, Builds: map[string]string{}},
		Records:   map[string]router.DeploymentRecord{},
	}
	for _, record := range records {
		move.Promotion.Builds[record.App] = record.Build
		move.Records[record.App] = record
	}
	return move
}

func TestAPointerMoveServesItsRecordsOnItsPointerInTheStore(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")

	movePointer(t, p, state, pointerMoveOf("promo-1", "", router.DeploymentRecord{App: "web", Build: "b1"}))
	movePointer(t, p, state, pointerMoveOf("promo-preview", "pr-42", router.DeploymentRecord{App: "web", Build: "b2"}))
	movePointer(t, p, state, pointerMoveOf("promo-2", "", router.DeploymentRecord{App: "web", Build: "b3"}))

	if served := store.serving("", "web"); served != "b3" {
		t.Errorf("the default pointer serves %q, want b3, the last promotion moved onto it", served)
	}
	if served := store.serving("pr-42", "web"); served != "b2" {
		t.Errorf("pr-42 serves %q, want b2: a pointer move moves only the pointer it names", served)
	}
	sent := store.moved()
	if last := sent[len(sent)-1]; last.Replaces != "promo-1" {
		t.Errorf("the last pointer move replaced %q, want promo-1, the promotion the store served when it read the pointer", last.Replaces)
	}
	if sent[1].Pointer != "pr-42" || sent[0].Pointer != "" {
		t.Errorf("moves named pointers %q and %q, want the preview pointer sent and the default one left out", sent[1].Pointer, sent[0].Pointer)
	}
}

func TestRemovingAPointerLeavesNothingServedOnIt(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")
	movePointer(t, p, state, pointerMoveOf("promo-preview", "pr-42", router.DeploymentRecord{App: "web", Build: "b1"}))

	if err := (routerStack{s: stackOn(p, state)}).RemovePointer(t.Context(), router.PointerRemoval{Pointer: "pr-42"}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if served := store.serving("pr-42", "web"); served != "" {
		t.Errorf("pr-42 serves %q after it was removed, want nothing", served)
	}
}

func TestStoreRequest(t *testing.T) {
	t.Parallel()

	t.Run("a state with no endpoint is an error", func(t *testing.T) {
		t.Parallel()

		_, err := stackOn(&cloudflare{}, edge.StackState{}).readServedPromotion(t.Context(), "")
		if err == nil {
			t.Fatal("expected an error when the root-stack state has no endpoint")
		}
	})

	t.Run("an unavailable store is retried until it answers", func(t *testing.T) {
		t.Parallel()

		attempts := 0
		mux := http.NewServeMux()
		mux.HandleFunc("GET /{slug}/pointer", func(w http.ResponseWriter, _ *http.Request) {
			attempts++
			if attempts < 3 {
				w.Header().Set("Retry-After-Ms", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"promotionId":null}`))
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		if _, err := stackOn(&cloudflare{}, testState(srv.URL, "s3cr3t")).readServedPromotion(t.Context(), ""); err != nil {
			t.Fatalf("readServedPromotion: %v", err)
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

		if _, err := stackOn(&cloudflare{}, testState(srv.URL, "wrong")).readServedPromotion(t.Context(), ""); err == nil {
			t.Fatal("readServedPromotion err = nil, want the rejection surfaced")
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

		if _, err := stackOn(&cloudflare{}, testState(srv.URL, "s3cr3t")).readServedPromotion(ctx, ""); err == nil {
			t.Fatal("readServedPromotion err = nil, want the failure surfaced")
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
		v, _, err := (&cloudflare{}).getVersionStamp(t.Context(), srv.URL, "acme-web", "s3cr3t")
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
		p := &cloudflare{}
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

	t.Run("a state with no secret is a no-op", func(t *testing.T) {
		t.Parallel()

		if err := (&cloudflare{}).destroyInstance(t.Context(), edge.StackState{}); err != nil {
			t.Fatalf("destroyInstance(empty) err = %v, want nil", err)
		}
	})

	t.Run("the instance is wiped and stops honouring its secret", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &cloudflare{}
		state := testState(srv.URL, "s3cr3t")
		movePointer(t, p, state, pointerMoveOf("p1", "", router.DeploymentRecord{App: "web", Build: "b1"}))
		if err := p.destroyInstance(t.Context(), state); err != nil {
			t.Fatalf("destroyInstance: %v", err)
		}
		if _, err := stackOn(p, state).readServedPromotion(t.Context(), ""); err == nil {
			t.Error("reading the pointer after destroy: err = nil, want the wiped instance to reject the secret")
		}
	})

	t.Run("an already-wiped instance is a success", func(t *testing.T) {
		t.Parallel()

		srv := fakeStoreServer(t, "s3cr3t")
		p := &cloudflare{}
		state := testState(srv.URL, "s3cr3t")
		if err := p.destroyInstance(t.Context(), state); err != nil {
			t.Fatalf("destroyInstance: %v", err)
		}
		if err := p.destroyInstance(t.Context(), state); err != nil {
			t.Fatalf("destroyInstance on an already-wiped instance: err = %v, want nil", err)
		}
	})
}

func TestAPreviewPointerMoveServesEachOfItsHostsByItsLabel(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")
	move := pointerMoveOf("promo-preview", "pr-42",
		router.DeploymentRecord{App: "web", Build: "b1"},
		router.DeploymentRecord{App: "admin", Build: "b2"})
	move.Hosts = []edge.PreviewHost{
		{Hostname: "pr-42-web-aaaaaaaaaaaaaaaabbbbbbbb.preview.example.com", App: "web"},
		{Hostname: "pr-42-admin-ccccccccccccccccdddddddd.preview.example.com", App: "admin"},
	}

	movePointer(t, p, state, move)

	if served := store.findServedBuild("pr-42-web-aaaaaaaaaaaaaaaabbbbbbbb"); served != "b1" {
		t.Errorf("the web preview label serves %q, want b1", served)
	}
	if served := store.findServedBuild("pr-42-admin-ccccccccccccccccdddddddd"); served != "b2" {
		t.Errorf("the admin preview label serves %q, want b2", served)
	}
}

func TestRemovingAPreviewPointerStopsServingItsLabels(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")
	hosts := []edge.PreviewHost{{Hostname: "pr-42-aaaaaaaaaaaaaaaabbbbbbbb.preview.example.com"}}
	move := pointerMoveOf("promo-preview", "pr-42", router.DeploymentRecord{App: "web", Build: "b1"})
	move.Hosts = hosts
	movePointer(t, p, state, move)

	if err := (routerStack{s: stackOn(p, state)}).RemovePointer(t.Context(), router.PointerRemoval{Pointer: "pr-42", Hosts: hosts}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if served := store.findServedBuild("pr-42-aaaaaaaaaaaaaaaabbbbbbbb"); served != "" {
		t.Errorf("the preview label serves %q after its pointer was removed, want nothing", served)
	}
}
