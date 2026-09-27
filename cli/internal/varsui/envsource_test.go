package varsui_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type fakeEnvSource struct {
	store     *fakeStore
	described envgate.EnvSource
	awaiting  bool
	refusal   error
	syncError error

	describes int
	syncs     int
	created   []createdValue
	pending   map[envgate.Cell]string
}

type createdValue struct {
	at          envgate.Cell
	value       string
	description string
}

func (f *fakeEnvSource) Describe(context.Context) (envgate.EnvSource, error) {
	f.describes++
	return f.described, nil
}

func (f *fakeEnvSource) Sync(context.Context) error {
	if f.syncError != nil {
		return f.syncError
	}
	f.syncs++
	for cell, value := range f.pending {
		f.store.cells[cell] = value
	}
	return nil
}

func (f *fakeEnvSource) Create(_ context.Context, at envgate.Cell, value, description string) (bool, error) {
	if f.refusal != nil {
		return false, f.refusal
	}
	f.created = append(f.created, createdValue{at: at, value: value, description: description})
	if !f.awaiting {
		f.store.cells[at] = value
	}
	return f.awaiting, nil
}

var infisical = envgate.EnvSource{
	ID:          "infisical:p-1/prod",
	Writable:    true,
	URLs:        map[string]string{"": "https://infisical.example/root"},
	Credentials: []string{"INFISICAL_CLIENT_ID"},
}

func envSourceSession(t *testing.T, source *fakeEnvSource, recovery *varsui.Recovery) *varsui.Session {
	t.Helper()
	described := def("API_URL")
	described.Description = "where the API lives"
	gate := envgate.New(source.store, envgate.Scope{
		Apps:      []envgate.App{{Name: "web", Folder: "/web"}, {Name: "api"}},
		EnvSource: envgate.EnvSource{ID: infisical.ID, Credentials: infisical.Credentials},
	})
	if err := gate.Prefetch(context.Background()); err != nil {
		t.Fatalf("Prefetch: %v", err)
	}
	if _, err := gate.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{described}}); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	return serveWith(t, context.Background(), varsui.Options{
		Gate:      gate,
		Store:     source.store,
		EnvSource: source,
		Recovery:  recovery,
	})
}

func createIn(t *testing.T, s *varsui.Session, body map[string]string) *http.Response {
	t.Helper()
	return request(t, s, http.MethodPost, "/api/env-source/value", body)
}

func TestThePageNamesTheEnvSourceTheTierReadsFrom(t *testing.T) {
	t.Parallel()

	t.Run("the state names it, where each folder lives there and what it logs in with", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical}
		got := state(t, envSourceSession(t, source, nil)).EnvSource

		want := varsui.EnvSource{ID: infisical.ID, Writable: true, URLs: infisical.URLs, Credentials: infisical.Credentials}
		if got == nil || !reflect.DeepEqual(*got, want) {
			t.Errorf("env source = %+v, want %+v", got, want)
		}
	})

	t.Run("reading the page describes the env source and never syncs it", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical}
		s := envSourceSession(t, source, nil)
		state(t, s)
		state(t, s)

		if source.describes != 2 || source.syncs != 0 {
			t.Errorf("describes = %d, syncs = %d, want 2 and 0", source.describes, source.syncs)
		}
	})

	t.Run("a session with no env source names none", func(t *testing.T) {
		t.Parallel()
		body := bodyOf(t, request(t, session(t, newFakeStore(), def("API_URL")), http.MethodGet, "/api/state", nil))
		if strings.Contains(body, `"envSource":{`) {
			t.Errorf("state = %s, want no env source", body)
		}
	})
}

func TestAValueTheEnvSourceLacksIsCreatedThere(t *testing.T) {
	t.Parallel()

	t.Run("with the description its declaration gives", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical}
		s := envSourceSession(t, source, nil)

		resp := createIn(t, s, map[string]string{"key": "API_URL", "folder": "", "value": "https://api.example"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}
		want := []createdValue{{at: envgate.Cell{Key: "API_URL"}, value: "https://api.example", description: "where the API lives"}}
		if !reflect.DeepEqual(source.created, want) {
			t.Errorf("created %+v, want %+v", source.created, want)
		}
		if !cellState(t, state(t, s), "API_URL", "").Set {
			t.Errorf("API_URL reads as unset after its value was created")
		}
	})

	t.Run("and a create waiting for approval there says so", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical, awaiting: true}
		resp := createIn(t, envSourceSession(t, source, nil), map[string]string{"key": "API_URL", "value": "https://api.example"})

		var out struct {
			AwaitingApproval bool `json:"awaitingApproval"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK || !out.AwaitingApproval {
			t.Errorf("POST = %d %+v (%v), want the approval named", resp.StatusCode, out, err)
		}
	})

	t.Run("and the env source's own refusal is the answer", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical, refusal: errors.New("infisical:p-1/prod already has API_URL")}
		resp := createIn(t, envSourceSession(t, source, nil), map[string]string{"key": "API_URL", "value": "x"})

		if body := bodyOf(t, resp); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "already has API_URL") {
			t.Errorf("POST = %d %q, want the env source's reason", resp.StatusCode, body)
		}
	})
}

func TestAValueTheEnvSourceMayNotHoldIsRefusedBeforeAnythingIsCreatedThere(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body map[string]string
		want string
	}{
		{"one for a single named environment, which ocel stores", map[string]string{"key": "API_URL", "environment": "pr-12", "value": "x"}, "pr-12"},
		{"one nothing declares", map[string]string{"key": "STRAY", "value": "x"}, "STRAY"},
		{"a credential the env source is logged in with", map[string]string{"key": "INFISICAL_CLIENT_ID", "value": "x"}, "INFISICAL_CLIENT_ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := &fakeEnvSource{store: newFakeStore(), described: infisical}
			resp := createIn(t, envSourceSession(t, source, nil), tc.body)

			if body := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Errorf("POST = %d %q, want %d naming %s", resp.StatusCode, body, http.StatusBadRequest, tc.want)
			}
			if len(source.created) != 0 {
				t.Errorf("created %+v, want nothing sent to the env source", source.created)
			}
		})
	}

	t.Run("one in a session with no env source", func(t *testing.T) {
		t.Parallel()
		resp := createIn(t, session(t, newFakeStore(), def("API_URL")), map[string]string{"key": "API_URL", "value": "x"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})
}

func TestResumingADeploySyncsTheEnvSourceFirst(t *testing.T) {
	t.Parallel()

	t.Run("so a value set in the env source's own dashboard counts", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		source := &fakeEnvSource{store: store, described: infisical, pending: map[envgate.Cell]string{{Key: "API_URL"}: "https://set-there.example"}}
		s := envSourceSession(t, source, &varsui.Recovery{Deploy: "ocel deploy", Missing: []envgate.Cell{{Key: "API_URL"}}})

		if resp := request(t, s, http.MethodPost, "/api/done", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/done = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}
		if source.syncs != 1 {
			t.Errorf("syncs = %d, want one before the gate is checked", source.syncs)
		}
	})

	t.Run("and a failed sync keeps the deploy waiting", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical, syncError: errors.New("infisical:p-1/prod: 503")}
		s := envSourceSession(t, source, &varsui.Recovery{Deploy: "ocel deploy", Missing: []envgate.Cell{{Key: "API_URL"}}})

		if resp := request(t, s, http.MethodPost, "/api/done", nil); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("POST /api/done = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
		if err := s.Wait(shortContext(t)); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Wait = %v, want the session still open", err)
		}
	})

	t.Run("while leaving a page no deploy waits on syncs nothing", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), described: infisical}
		s := envSourceSession(t, source, nil)

		if resp := request(t, s, http.MethodPost, "/api/done", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/done = %d", resp.StatusCode)
		}
		if source.syncs != 0 {
			t.Errorf("syncs = %d, want none", source.syncs)
		}
	})
}
