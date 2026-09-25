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
)

type fakeEnvSource struct {
	store    *fakeStore
	status   varsui.EnvSource
	awaiting bool
	failure  error

	created []created
	syncs   int
	pending map[envgate.Cell]string
}

type created struct {
	at          envgate.Address
	value       string
	description string
}

func (f *fakeEnvSource) Describe(context.Context) (varsui.EnvSource, error) {
	return f.status, nil
}

func (f *fakeEnvSource) Sync(context.Context) error {
	f.syncs++
	for cell, value := range f.pending {
		f.store.cells[cell] = value
	}
	return nil
}

func (f *fakeEnvSource) Create(_ context.Context, at envgate.Address, value, description string) (bool, error) {
	if f.failure != nil {
		return false, f.failure
	}
	f.created = append(f.created, created{at: at, value: value, description: description})
	if !f.awaiting {
		f.store.cells[at.Cell] = value
	}
	return f.awaiting, nil
}

var infisical = varsui.EnvSource{
	ID:          "infisical:p-1/prod",
	Writable:    true,
	Links:       map[string]string{"": "https://infisical.example/root"},
	Credentials: []string{"INFISICAL_CLIENT_ID"},
}

func sourcedSession(t *testing.T, source *fakeEnvSource, recovery *varsui.Recovery) *varsui.Session {
	t.Helper()
	described := def("API_URL")
	described.Description = "where the API lives"
	return serveWith(t, context.Background(), varsui.Options{
		Gate:      discovered(t, source.store, described),
		Store:     source.store,
		EnvSource: source,
		Recovery:  recovery,
	})
}

func TestEnvSource(t *testing.T) {
	t.Parallel()

	t.Run("the state names the tier's env source, its links and its credentials", func(t *testing.T) {
		t.Parallel()
		s := sourcedSession(t, &fakeEnvSource{store: newFakeStore(), status: infisical}, nil)

		got := state(t, s).EnvSource
		if got == nil || !reflect.DeepEqual(*got, infisical) {
			t.Errorf("env source = %+v, want %+v", got, infisical)
		}
	})

	t.Run("a session with no env source names none", func(t *testing.T) {
		t.Parallel()
		body := bodyOf(t, request(t, session(t, newFakeStore(), def("API_URL")), http.MethodGet, "/api/state", nil))
		if strings.Contains(body, "envSource\":{") {
			t.Errorf("state = %s, want no env source", body)
		}
	})

	t.Run("a value created in the source carries the declaration's description", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), status: infisical}
		s := sourcedSession(t, source, nil)

		resp := request(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_URL", "folder": "", "value": "https://api.example"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}
		want := []created{{at: envgate.Address{Cell: envgate.Cell{Key: "API_URL"}}, value: "https://api.example", description: "where the API lives"}}
		if !reflect.DeepEqual(source.created, want) {
			t.Errorf("created %+v, want %+v", source.created, want)
		}
	})

	t.Run("a create the source holds for approval says so", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), status: infisical, awaiting: true}
		s := sourcedSession(t, source, nil)

		resp := request(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_URL", "value": "https://api.example"})
		var out struct {
			AwaitingApproval bool `json:"awaitingApproval"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.AwaitingApproval {
			t.Errorf("POST = %d %+v (%v), want the approval named", resp.StatusCode, out, err)
		}
	})

	t.Run("a value for one named environment is ocel's to hold, never the source's", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), status: infisical}
		s := sourcedSession(t, source, nil)

		resp := request(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_URL", "environment": "pr-12", "value": "x"})
		if body := bodyOf(t, resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "pr-12") {
			t.Errorf("POST = %d %q, want %d naming the environment", resp.StatusCode, body, http.StatusBadRequest)
		}
		if len(source.created) != 0 {
			t.Errorf("created %+v, want nothing sent to the source", source.created)
		}
	})

	t.Run("an undeclared key is never created in the source", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), status: infisical}
		s := sourcedSession(t, source, nil)

		resp := request(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "STRAY", "value": "x"})
		if resp.StatusCode != http.StatusBadRequest || len(source.created) != 0 {
			t.Errorf("POST = %d, created %+v, want a refusal and nothing sent", resp.StatusCode, source.created)
		}
	})

	t.Run("a create the source refuses answers with its reason", func(t *testing.T) {
		t.Parallel()
		source := &fakeEnvSource{store: newFakeStore(), status: infisical, failure: errors.New("infisical:p-1/prod already holds API_URL")}
		s := sourcedSession(t, source, nil)

		resp := request(t, s, http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_URL", "value": "x"})
		if body := bodyOf(t, resp); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "already holds API_URL") {
			t.Errorf("POST = %d %q, want the source's reason", resp.StatusCode, body)
		}
	})

	t.Run("a session with no env source creates nothing", func(t *testing.T) {
		t.Parallel()
		resp := request(t, session(t, newFakeStore(), def("API_URL")), http.MethodPost, "/api/env-source/value", map[string]string{"key": "API_URL", "value": "x"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}
	})

	t.Run("resuming a deploy reads the source again first, so a value set there counts", func(t *testing.T) {
		t.Parallel()
		store := newFakeStore()
		source := &fakeEnvSource{store: store, status: infisical, pending: map[envgate.Cell]string{{Key: "API_URL"}: "https://set-in-infisical.example"}}
		s := sourcedSession(t, source, &varsui.Recovery{Deploy: "ocel deploy", Owed: []envgate.Cell{{Key: "API_URL"}}})

		if resp := request(t, s, http.MethodPost, "/api/done", nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/done = %d: %s", resp.StatusCode, bodyOf(t, resp))
		}
		if source.syncs != 1 {
			t.Errorf("syncs = %d, want one before the gate is checked", source.syncs)
		}
	})
}
