package providerkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func registryServing(t *testing.T, handler http.HandlerFunc) (providerkit.ImageStore, providerkit.ImagePush) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	target := providerkit.RegistryTarget{Server: host, Namespace: "acme", Username: "acme-bot", Password: "hunter2"}
	return providerkit.RegistryImages(target), providerkit.ImagePush{
		App:    "web",
		Source: "ocel/web@sha256:abc",
		Target: target.Coordinate("web", "sha256-abc"),
		Digest: "sha256:abc",
	}
}

func TestADigestTagTheRegistryAnswersForIsAlreadyThere(t *testing.T) {
	var asked string
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method + " " + r.URL.Path
	})

	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !held {
		t.Error("Has() said no for a manifest the registry answers for, so the deploy would push what is already there")
	}
	if asked != "HEAD /v2/acme/web/manifests/sha256-abc" {
		t.Errorf("Has() asked %q, want a head of the digest tag's manifest", asked)
	}
}

func TestADigestTagTheRegistryDoesNotHoldIsPushed(t *testing.T) {
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "manifest unknown", http.StatusNotFound)
	})

	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if held {
		t.Error("Has() said yes for a manifest the registry does not hold")
	}
}

func TestABearerChallengeIsAnsweredWithATokenBoughtWithTheRegistryPassword(t *testing.T) {
	var bought, presented string
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			user, password, _ := r.BasicAuth()
			bought = user + ":" + password + "?" + r.URL.RawQuery
			w.Write([]byte(`{"token":"a-scoped-token"}`))
			return
		}
		presented = r.Header.Get("Authorization")
		if presented == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry",scope="repository:acme/web:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	})

	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !held {
		t.Error("Has() gave up on a challenge it was handed a token for")
	}
	if !strings.HasPrefix(bought, "acme-bot:hunter2?") || !strings.Contains(bought, "scope=repository%3Aacme%2Fweb%3Apull") {
		t.Errorf("the token was bought as %q, want the deploy's own credentials against the scope the registry named", bought)
	}
	if presented != "Bearer a-scoped-token" {
		t.Errorf("the retry presented %q, want the token the registry handed out", presented)
	}
}

func TestABasicChallengeIsAnsweredWithTheRegistryPassword(t *testing.T) {
	var presented string
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		user, password, given := r.BasicAuth()
		if !given {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		presented = user + ":" + password
	})

	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !held || presented != "acme-bot:hunter2" {
		t.Errorf("Has() authenticated as %q, want the deploy's own credentials", presented)
	}
}

func TestARegistryThatAnswersNeitherWayStopsTheDeployRatherThanPushing(t *testing.T) {
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the registry is down", http.StatusInternalServerError)
	})

	if _, err := store.Has(context.Background(), push); err == nil {
		t.Fatal("Has() read a broken registry as an empty one, and a plan would show a push it cannot verify")
	}
}

func TestAThrottledRegistryIsWaitedOutRatherThanTreatedAsEmpty(t *testing.T) {
	asked := 0
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		asked++
		if asked == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
	})

	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if !held || asked != 2 {
		t.Errorf("Has() asked %d times and answered %v; a throttle is an expected answer, not a missing image", asked, held)
	}
}
