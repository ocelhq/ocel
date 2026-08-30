package providerkit_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestAManifestTheRegistryAnswersForUnderAnotherDigestIsNotThisImage(t *testing.T) {
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Content-Digest", "sha256:someoneelses")
	})

	held, err := store.Has(context.Background(), push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if held {
		t.Error("Has() read a tag holding a foreign digest as this image, so the release would point a container at whatever was there")
	}
}

func TestARealmOnAnotherHostIsNotHandedTheRegistryPassword(t *testing.T) {
	var harvested string
	thief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, _ := r.BasicAuth()
		harvested = password
		w.Write([]byte(`{"token":"stolen"}`))
	}))
	t.Cleanup(thief.Close)

	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+thief.URL+`/token",service="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	})

	if _, err := store.Has(context.Background(), push); err == nil {
		t.Fatal("Has() bought a token from a realm the registry named on another host, so a hostile registry names anywhere and is handed the deploy's password")
	}
	if harvested != "" {
		t.Errorf("the foreign realm was handed %q", harvested)
	}
}

func TestAPlainHTTPRealmIsHandedTheRegistryPasswordOnlyOnLoopback(t *testing.T) {
	if err := providerkit.CredentialsTravelTo("http://ghcr.io/token", "ghcr.io"); err == nil {
		t.Error("a plain-http realm on an https registry was accepted, so the deploy's password would cross the wire in clear")
	}
	if err := providerkit.CredentialsTravelTo("http://elsewhere.invalid/token", "ghcr.io"); err == nil {
		t.Error("a plain-http realm on another host was accepted")
	}
	if err := providerkit.CredentialsTravelTo("token", "ghcr.io"); err == nil {
		t.Error("a realm naming no scheme was accepted")
	}
	if err := providerkit.CredentialsTravelTo("https://auth.docker.io/token", "registry-1.docker.io"); err != nil {
		t.Errorf("the https realm a registry delegates to was refused: %v", err)
	}
	if err := providerkit.CredentialsTravelTo("http://127.0.0.1:5000/token", "127.0.0.1:5000"); err != nil {
		t.Errorf("a loopback registry's own plain-http realm was refused: %v", err)
	}
}

func TestAScopeCarryingACommaSurvivesTheChallenge(t *testing.T) {
	var bought string
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			bought = r.URL.Query().Get("scope")
			w.Write([]byte(`{"token":"a-scoped-token"}`))
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="registry",scope="repository:acme/web:pull,push"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	})

	if _, err := store.Has(context.Background(), push); err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if bought != "repository:acme/web:pull,push" {
		t.Errorf("the token was bought for scope %q, want the whole scope the registry named", bought)
	}
}

func daemonServing(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	daemon := httptest.NewServer(handler)
	t.Cleanup(daemon.Close)
	t.Setenv(providerkit.DockerTLSVerifyEnv, "")
	t.Setenv(providerkit.DockerCertPathEnv, "")
	t.Setenv(providerkit.DockerHostEnv, "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	return daemon
}

func pushTo(target providerkit.RegistryTarget) (providerkit.ImageStore, providerkit.ImagePush) {
	return providerkit.RegistryImages(target), providerkit.ImagePush{
		App:    "web",
		Source: "ocel/web@sha256:abc",
		Target: target.Coordinate("web", "sha256-abc"),
		Digest: "sha256:abc",
	}
}

func TestThePushNamesTheImageRemotelyAndHandsTheDaemonTheDeploysCredentials(t *testing.T) {
	var named, pushed, unnamed, authorization string
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tag"):
			named = r.URL.Path + "?" + r.URL.RawQuery
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/push"):
			pushed = r.URL.Path + "?" + r.URL.RawQuery
			authorization = r.Header.Get("X-Registry-Auth")
			w.Write([]byte(`{"status":"Pushed"}`))
		case r.Method == http.MethodDelete:
			unnamed = r.URL.Path
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
	store, push := pushTo(providerkit.RegistryTarget{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot", Password: "hunter2"})

	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if named != "/images/ocel/web@sha256:abc/tag?repo=ghcr.io%2Facme%2Fweb&tag=sha256-abc" {
		t.Errorf("Push() named the image with %q, want the built image tagged at the coordinate it is released under", named)
	}
	if pushed != "/images/ghcr.io/acme/web/push?tag=sha256-abc" {
		t.Errorf("Push() asked the daemon for %q, want a push of the remote coordinate", pushed)
	}
	if unnamed != "/images/ghcr.io/acme/web:sha256-abc" {
		t.Errorf("Push() left %q behind on the machine's own daemon, want the remote coordinate removed once the push is done", unnamed)
	}
	decoded, err := base64.URLEncoding.DecodeString(authorization)
	if err != nil {
		t.Fatalf("the daemon was handed %q as its registry auth: %v", authorization, err)
	}
	var carried map[string]string
	if err := json.Unmarshal(decoded, &carried); err != nil {
		t.Fatalf("the daemon was handed %q as its registry auth: %v", decoded, err)
	}
	if carried["username"] != "acme-bot" || carried["password"] != "hunter2" || carried["serveraddress"] != "ghcr.io" {
		t.Errorf("the daemon was handed %v, want the deploy's own credentials for the registry it pushes to", carried)
	}
}

func TestAnAnonymousTargetHandsTheDaemonNoCredentialsAtAll(t *testing.T) {
	var carried string
	var sent bool
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/push") {
			carried, sent = r.Header.Get("X-Registry-Auth"), r.Header.Get("X-Registry-Auth") != ""
			w.Write([]byte(`{"status":"Pushed"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	store, push := pushTo(providerkit.RegistryTarget{Server: "127.0.0.1:5000"})

	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v", err)
	}
	if sent {
		t.Errorf("Push() handed the daemon %q for a registry the deploy has no credentials for", carried)
	}
}

func TestARegistryThatRefusesThePushStopsTheRelease(t *testing.T) {
	attempts := 0
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/push") {
			attempts++
			w.Write([]byte(`{"error":"denied: requested access to the resource is denied"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	store, push := pushTo(providerkit.RegistryTarget{Server: "ghcr.io", Username: "acme-bot", Password: "hunter2"})

	err := store.Push(context.Background(), push, nil)
	if err == nil {
		t.Fatal("Push() read a refusal in the progress stream as a finished push")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("Push() = %v, want the refusal the registry gave", err)
	}
	if attempts != 1 {
		t.Errorf("Push() tried %d times against credentials the registry refuses, want one", attempts)
	}
}

func TestAThrottledPushIsWaitedOutRatherThanFailingTheDeploy(t *testing.T) {
	attempts := 0
	daemonServing(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/push") {
			attempts++
			if attempts == 1 {
				w.Write([]byte(`{"error":"toomanyrequests: You have reached your pull rate limit"}`))
				return
			}
			w.Write([]byte(`{"status":"Pushed"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	store, push := pushTo(providerkit.RegistryTarget{Server: "ghcr.io", Username: "acme-bot", Password: "hunter2"})

	if err := store.Push(context.Background(), push, nil); err != nil {
		t.Fatalf("Push() = %v, and a throttle is an answer to wait out, not a failed deploy", err)
	}
	if attempts != 2 {
		t.Errorf("Push() tried %d times, want the throttled attempt retried", attempts)
	}
}

func TestARegistryThatNeverAnswersStopsTheDeployRatherThanHangingIt(t *testing.T) {
	store, push := registryServing(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	held := *providerkit.RegistryTimeout
	*providerkit.RegistryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { *providerkit.RegistryTimeout = held })

	done := make(chan error, 1)
	go func() {
		_, err := store.Has(context.Background(), push)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Has() answered a registry that never answered")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Has() is still waiting on a registry that accepted the connection and said nothing, and only Ctrl-C ends the deploy")
	}
}

func TestAHostnameThatResolvesToNothingIsReportedRatherThanRetried(t *testing.T) {
	if providerkit.Addressable(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Error("a hostname that resolves to nothing is retried five times, so a typo takes seconds to report")
	}
	if !providerkit.Addressable(errors.New("connection reset by peer")) {
		t.Error("a transport error the next attempt might survive is not retried")
	}
}
