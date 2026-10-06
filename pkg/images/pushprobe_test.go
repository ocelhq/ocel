package images_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type uploads struct {
	mu        sync.Mutex
	scopes    []string
	started   []string
	cancelled []string
}

func (u *uploads) record(list *[]string, value string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	*list = append(*list, value)
}

func pushRegistry(t *testing.T, u *uploads, upload http.HandlerFunc) provider.RegistryTarget {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			u.record(&u.scopes, r.URL.Query().Get("scope"))
			if user, pass, _ := r.BasicAuth(); user != "acme-bot" || pass != "hunter2" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"pushes"}`))
		case r.Header.Get("Authorization") != "Bearer pushes":
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry",scope="repository:acme/web:pull,push"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/acme/web/blobs/uploads/":
			u.record(&u.started, r.URL.Path)
			upload(w, r)
		case r.Method == http.MethodDelete:
			u.record(&u.cancelled, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return provider.RegistryTarget{Server: strings.TrimPrefix(server.URL, "http://"), Namespace: "acme", Username: "acme-bot", Password: "hunter2"}
}

func TestPushAccessStartsAnUploadWithAPushTokenAndCancelsIt(t *testing.T) {
	t.Parallel()

	u := &uploads{}
	target := pushRegistry(t, u, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/v2/acme/web/blobs/uploads/session-1")
		w.WriteHeader(http.StatusAccepted)
	})

	if err := images.ProbePushAccess(context.Background(), target, "web"); err != nil {
		t.Fatalf("ProbePushAccess() error = %v, want a registry that accepts the upload to grant push", err)
	}
	if len(u.scopes) != 1 || u.scopes[0] != "repository:acme/web:pull,push" {
		t.Errorf("token scopes = %q, want one token asked for push on acme/web", u.scopes)
	}
	if len(u.started) != 1 {
		t.Errorf("uploads started = %q, want one", u.started)
	}
	if len(u.cancelled) != 1 || u.cancelled[0] != "/v2/acme/web/blobs/uploads/session-1" {
		t.Errorf("uploads cancelled = %q, want the one started, so nothing is left behind", u.cancelled)
	}
}

func TestPushAccessHandsAnUploadLocationOnAnotherHostNoCredential(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var stolen []string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stolen = append(stolen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(elsewhere.Close)
	target := pushRegistry(t, &uploads{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/v2/acme/web/blobs/uploads/session-1")
		w.WriteHeader(http.StatusAccepted)
	})

	if err := images.ProbePushAccess(context.Background(), target, "web"); err != nil {
		t.Fatalf("ProbePushAccess() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, authorization := range stolen {
		if authorization != "" {
			t.Errorf("the host the upload's Location names was handed %q, want the registry's credential kept on the registry", authorization)
		}
	}
}

func TestPushAccessRefusedByTheRegistryIsADeniedCredentialNamingWhatTheRegistrySaid(t *testing.T) {
	t.Parallel()

	target := pushRegistry(t, &uploads{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"code":"DENIED","message":"permission_denied: create_package"}]}`))
	})

	err := images.ProbePushAccess(context.Background(), target, "web")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("ProbePushAccess() error = %v, want a denied refusal", err)
	}
	for _, want := range []string{"acme-bot", target.Server + "/acme/web", "DENIED: permission_denied: create_package"} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("refusal = %q, want it to name %q", refused.Message, want)
		}
	}
}

func TestPushAccessWithAPasswordTheTokenRealmRejectsIsADeniedCredential(t *testing.T) {
	t.Parallel()

	target := pushRegistry(t, &uploads{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	target.Password = "wrong"

	err := images.ProbePushAccess(context.Background(), target, "web")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("ProbePushAccess() error = %v, want a denied refusal", err)
	}
}

func TestPushAccessToARegistryThatCannotBeReachedIsNotReady(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.NotFoundHandler())
	host := strings.TrimPrefix(server.URL, "http://")
	server.Close()

	err := images.ProbePushAccess(context.Background(), provider.RegistryTarget{Server: host, Namespace: "acme"}, "web")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("ProbePushAccess() error = %v, want a not-ready refusal", err)
	}
}
