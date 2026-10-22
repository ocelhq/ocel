package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	storeBootstrapCredential = "bootstrap-cred"
)

type labelTarget struct{ pointer, app string }

type fakeStore struct {
	mu       sync.Mutex
	pointers map[string]string
	served   map[string]map[string]router.ReleaseRecord
	labels   map[string]labelTarget
	moves    []pointerMoveBody
	version  *string
	gate     *gate.Gate
	gateless bool
	live     string
}

func (f *fakeStore) serving(pointer, app string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pointer == "" {
		pointer = router.DefaultPointer
	}
	return f.served[pointer][app].Release
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
			return record.Release
		}
	}
	return served[at.app].Release
}

func (f *fakeStore) dropLabels(pointer string) {
	for label, at := range f.labels {
		if at.pointer == pointer {
			delete(f.labels, label)
		}
	}
}

func (f *fakeStore) findDifferentRecord(records []servedRecord) (router.ReleaseRecord, bool) {
	for _, record := range records {
		for _, served := range f.served {
			held, found := served[record.App]
			if found && held.Release == record.Release && !reflect.DeepEqual(held, record.ReleaseRecord) {
				return record.ReleaseRecord, true
			}
		}
	}
	return router.ReleaseRecord{}, false
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
		served:   map[string]map[string]router.ReleaseRecord{},
		labels:   map[string]labelTarget{},
		live:     secret,
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
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Secret == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.live != "" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.live = body.Secret
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
		if changed, found := f.findDifferentRecord(body.Records); found {
			http.Error(w, fmt.Sprintf("release %s of %s is already served with another record", changed.Release, changed.App), http.StatusUnprocessableEntity)
			return
		}
		f.moves = append(f.moves, body)
		f.served[pointer] = map[string]router.ReleaseRecord{}
		for _, record := range body.Records {
			f.served[pointer][record.App] = record.ReleaseRecord
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
	mux.HandleFunc("GET /{slug}/gate", authed(func(w http.ResponseWriter, r *http.Request) {
		if f.gateless {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]*gate.Gate{"gate": f.gate})
	}))
	mux.HandleFunc("PUT /{slug}/gate", authed(func(w http.ResponseWriter, r *http.Request) {
		if f.gateless {
			http.NotFound(w, r)
			return
		}
		var published gate.Gate
		if err := json.NewDecoder(r.Body).Decode(&published); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.gate = &published
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("DELETE /{slug}/gate", authed(func(w http.ResponseWriter, r *http.Request) {
		if f.gateless {
			http.NotFound(w, r)
			return
		}
		f.gate = nil
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /{slug}/destroy", authed(func(w http.ResponseWriter, _ *http.Request) {
		f.pointers = map[string]string{}
		f.served = map[string]map[string]router.ReleaseRecord{}
		f.labels = map[string]labelTarget{}
		f.version = nil
		f.gate = nil
		f.live = ""
		w.WriteHeader(http.StatusNoContent)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func stackOn(p *cloudflare, state edge.StackState) *stack {
	if p.store.client == nil {
		p.store.client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	}
	s := &stack{p: p, state: state}
	if err := state.Private.Into(&s.own); err != nil {
		panic(err)
	}
	return s
}

func routerStackOn(p *cloudflare, state edge.StackState) routerStack {
	s := stackOn(p, state)
	return routerStack{store: p.store, state: s.state, own: s.own}
}

func testState(endpoint, secret string) edge.StackState {
	return edge.StackState{
		Slug:     "acme-web",
		Endpoint: endpoint,
		Secret:   secret,
		Tier:     environment.TierProduction,
	}
}

func movePointer(t *testing.T, p *cloudflare, state edge.StackState, move router.PointerMove) {
	t.Helper()
	if err := routerStackOn(p, state).MovePointer(t.Context(), move, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(%s): %v", move.Promotion.PromotionID, err)
	}
}

func pointerMoveOf(promotionID, pointer string, records ...router.ReleaseRecord) router.PointerMove {
	move := router.PointerMove{
		Pointer:   pointer,
		Promotion: router.Promotion{PromotionID: promotionID, Ts: 1, Releases: map[string]string{}},
		Records:   map[string]router.ReleaseRecord{},
	}
	for _, record := range records {
		move.Promotion.Releases[record.App] = record.Release
		move.Records[record.App] = record
	}
	return move
}

func TestAPointerMoveTellsTheStoreTheReleaseTokenOfEveryReleaseTheLedgerStillKeepsOnThatPointer(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	web1, web2, api1 := keptReleaseOf(t, "a"), keptReleaseOf(t, "b"), keptReleaseOf(t, "c")
	move := pointerMoveOf("promo-2", "", router.ReleaseRecord{App: "web", Release: web2.String()})
	move.Kept = []router.Promotion{
		{PromotionID: "promo-2", Releases: map[string]string{"web": web2.String(), "api": api1.String()}},
		{PromotionID: "promo-1", Releases: map[string]string{"web": web1.String(), "api": api1.String()}},
	}

	movePointer(t, &cloudflare{}, testState(srv.URL, "s3cr3t"), move)

	want := []keptRelease{
		{App: "api", ReleaseToken: api1.Token().String()},
		{App: "web", ReleaseToken: web2.Token().String()},
		{App: "web", ReleaseToken: web1.Token().String()},
	}
	if got := store.moved()[0].Kept; !slices.Equal(got, want) {
		t.Errorf("the move kept %v, want %v: the edge knows a release by the token its ISR prefix names, and forgets what it holds for a release no rollback can reach", got, want)
	}
}

func TestAPointerMoveWhoseLedgerKeepsAReleaseWithNoTokenServesNothing(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	move := pointerMoveOf("promo-1", "", router.ReleaseRecord{App: "web", Release: keptReleaseOf(t, "a").String()})
	move.Kept = []router.Promotion{{PromotionID: "promo-1", Releases: map[string]string{"web": "not-a-release"}}}

	err := routerStackOn(&cloudflare{}, testState(srv.URL, "s3cr3t")).MovePointer(t.Context(), move, progress.Discard())

	if !errors.As(err, new(router.Unserved)) || len(store.moved()) != 0 {
		t.Errorf("MovePointer = %v after %d store move(s), want an unserved error and no move: a kept release the store cannot know by its token would be forgotten while a rollback can still serve it", err, len(store.moved()))
	}
}

func keptReleaseOf(t *testing.T, hexDigit string) provider.Release {
	t.Helper()
	buildID := strings.Repeat(hexDigit, 32)
	release, err := provider.NewRelease(buildID, "promo-"+hexDigit, "production", "")
	if err != nil {
		t.Fatalf("NewRelease(%q) = %v", buildID, err)
	}
	return release
}

func TestAPointerMoveServesItsRecordsOnItsPointerInTheStore(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")

	movePointer(t, p, state, pointerMoveOf("promo-1", "", router.ReleaseRecord{App: "web", Release: "b1"}))
	movePointer(t, p, state, pointerMoveOf("promo-preview", "pr-42", router.ReleaseRecord{App: "web", Release: "b2"}))
	movePointer(t, p, state, pointerMoveOf("promo-2", "", router.ReleaseRecord{App: "web", Release: "b3"}))

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

func TestAPointerMoveCarryingAnotherRecordForAServedReleaseIsUnservedAndNamesTheRelease(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")
	movePointer(t, p, state, pointerMoveOf("promo-1", "", router.ReleaseRecord{App: "web", Release: "b1", BuildID: "build-1"}))

	move := pointerMoveOf("promo-2", "", router.ReleaseRecord{App: "web", Release: "b1", BuildID: "build-2"})
	err := routerStackOn(p, state).MovePointer(t.Context(), move, progress.Discard())

	var unserved router.Unserved
	if !errors.As(err, &unserved) {
		t.Fatalf("MovePointer = %v, want router.Unserved: the store moved nothing, so the caller may unwind", err)
	}
	if !errors.Is(err, errServedRecordDiffers) {
		t.Errorf("MovePointer = %v, want errServedRecordDiffers: no retry or redeploy of this promotion can move the pointer", err)
	}
	if !strings.Contains(err.Error(), "release b1 of web") {
		t.Errorf("MovePointer = %q, want it to name release b1 of web", err)
	}
	if served := store.serving("", "web"); served != "b1" || store.served[router.DefaultPointer]["web"].BuildID != "build-1" {
		t.Errorf("the default pointer serves %q of build %q, want b1 of build-1 as before the refused move", served, store.served[router.DefaultPointer]["web"].BuildID)
	}
	if moves := len(store.moved()); moves != 1 {
		t.Errorf("the store took %d moves, want 1: a refused move is not retried", moves)
	}
}

func TestRemovingAPointerLeavesNothingServedOnIt(t *testing.T) {
	t.Parallel()

	srv, store := fakeStoreFor(t, "s3cr3t")
	p := &cloudflare{}
	state := testState(srv.URL, "s3cr3t")
	movePointer(t, p, state, pointerMoveOf("promo-preview", "pr-42", router.ReleaseRecord{App: "web", Release: "b1"}))

	if err := routerStackOn(p, state).RemovePointer(t.Context(), router.PointerRemoval{Pointer: "pr-42"}, progress.Discard()); err != nil {
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

		_, err := routerStackOn(&cloudflare{}, edge.StackState{}).readServedPromotion(t.Context(), "")
		if err == nil {
			t.Fatal("expected an error when the edge stack state has no endpoint")
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

		if _, err := routerStackOn(&cloudflare{}, testState(srv.URL, "s3cr3t")).readServedPromotion(t.Context(), ""); err != nil {
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

		if _, err := routerStackOn(&cloudflare{}, testState(srv.URL, "wrong")).readServedPromotion(t.Context(), ""); err == nil {
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

		if _, err := routerStackOn(&cloudflare{}, testState(srv.URL, "s3cr3t")).readServedPromotion(ctx, ""); err == nil {
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
		movePointer(t, p, state, pointerMoveOf("p1", "", router.ReleaseRecord{App: "web", Release: "b1"}))
		if err := p.destroyInstance(t.Context(), state); err != nil {
			t.Fatalf("destroyInstance: %v", err)
		}
		if _, err := routerStackOn(p, state).readServedPromotion(t.Context(), ""); err == nil {
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
		router.ReleaseRecord{App: "web", Release: "b1"},
		router.ReleaseRecord{App: "admin", Release: "b2"})
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
	move := pointerMoveOf("promo-preview", "pr-42", router.ReleaseRecord{App: "web", Release: "b1"})
	move.Hosts = hosts
	movePointer(t, p, state, move)

	if err := routerStackOn(p, state).RemovePointer(t.Context(), router.PointerRemoval{Pointer: "pr-42", Hosts: hosts}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if served := store.findServedBuild("pr-42-aaaaaaaaaaaaaaaabbbbbbbb"); served != "" {
		t.Errorf("the preview label serves %q after its pointer was removed, want nothing", served)
	}
}

func (f *fakeStore) withoutGateRoute() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gateless = true
}

func (f *fakeStore) heldGate() *gate.Gate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gate
}
