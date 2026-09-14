package front

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const healthPath = "/_ocel/health"

type upstream struct {
	server *httptest.Server

	host   string
	secret string
	seen   int
}

func serveUpstream(t *testing.T) *upstream {
	t.Helper()
	up := &upstream{}
	up.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.seen++
		up.host = r.Host
		up.secret = r.Header.Get(OriginSecretHeader)
		io.WriteString(w, "served by the app")
	}))
	t.Cleanup(up.server.Close)
	return up
}

func serveFront(t *testing.T, up *upstream, guard *Guard, ready func() bool) *httptest.Server {
	t.Helper()
	target, err := url.Parse(up.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(Handler(Options{
		Upstream:   target,
		Guard:      guard,
		HealthPath: healthPath,
		Ready:      ready,
	}))
	t.Cleanup(front.Close)
	return front
}

func ask(t *testing.T, front *httptest.Server, method, path, secret string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, front.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set(OriginSecretHeader, secret)
	}
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHandler(t *testing.T) {
	t.Run("a front with no guard serves anyone who reaches it", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, nil, nil)

		resp := ask(t, front, http.MethodGet, "/", "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d for a deployment with no edge in front of it", resp.StatusCode, http.StatusOK)
		}
	})

	t.Run("a guarded front turns away a request that carries no secret", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), nil)

		resp := ask(t, front, http.MethodGet, "/", "")
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want %d: a request that did not come through the edge is not the app's to answer", resp.StatusCode, http.StatusForbidden)
		}
		if up.seen != 0 {
			t.Error("the app was handed a request that came round the edge")
		}
	})

	t.Run("a guarded front serves the edge that presents the secret", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), nil)

		resp := ask(t, front, http.MethodGet, "/", "s3cr3t")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	})

	t.Run("a secret that is not the one this deployment was given is no secret", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), nil)

		for _, presented := range []string{"s3cr3", "s3cr3tt", "S3CR3T", "wrong"} {
			if resp := ask(t, front, http.MethodGet, "/", presented); resp.StatusCode != http.StatusForbidden {
				t.Errorf("status for %q = %d, want %d", presented, resp.StatusCode, http.StatusForbidden)
			}
		}
		if up.seen != 0 {
			t.Error("the app answered a request presenting a secret it was never given")
		}
	})

	t.Run("a guard built from an empty secret admits nobody", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard(""), nil)

		for _, presented := range []string{"", "s3cr3t"} {
			if resp := ask(t, front, http.MethodGet, "/", presented); resp.StatusCode != http.StatusForbidden {
				t.Errorf("status for %q = %d, want %d: an origin secret that was set to nothing fails closed", presented, resp.StatusCode, http.StatusForbidden)
			}
		}
		if up.seen != 0 {
			t.Error("an empty origin secret opened the app to anything that presented an empty header")
		}
	})

	t.Run("the health path is answered without the secret the rest of the app needs", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), nil)

		if resp := ask(t, front, http.MethodGet, healthPath, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d: the probe carries no secret and its failure takes the deployment down", resp.StatusCode, http.StatusOK)
		}
		if resp := ask(t, front, http.MethodHead, healthPath, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("HEAD status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	})

	t.Run("only a probe of the health path bypasses the guard", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), nil)

		if resp := ask(t, front, http.MethodPost, healthPath, ""); resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want %d: the path is a probe, not a way into the app", resp.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("nothing is served before the app is listening", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), func() bool { return false })

		for _, sent := range []struct{ path, secret string }{
			{"/", "s3cr3t"},
			{"/", ""},
			{healthPath, ""},
		} {
			resp := ask(t, front, http.MethodGet, sent.path, sent.secret)
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("status for %s = %d, want %d while the app is still starting", sent.path, resp.StatusCode, http.StatusServiceUnavailable)
			}
		}
		if up.seen != 0 {
			t.Error("a request reached the app before it was listening")
		}
	})

	t.Run("the app never sees the secret and is told the host the caller asked for", func(t *testing.T) {
		up := serveUpstream(t)
		front := serveFront(t, up, NewGuard("s3cr3t"), nil)

		ask(t, front, http.MethodGet, "/", "s3cr3t")

		if up.secret != "" {
			t.Errorf("the app was handed %s=%q; the secret is the front's business and nothing the app logs should carry it", OriginSecretHeader, up.secret)
		}
		if want := strings.TrimPrefix(front.URL, "http://"); up.host != want {
			t.Errorf("host = %q, want %q: the app builds absolute URLs from what the caller asked for", up.host, want)
		}
	})

	t.Run("an app that does not answer is reported as a bad gateway", func(t *testing.T) {
		up := serveUpstream(t)
		up.server.Close()
		front := serveFront(t, up, nil, nil)

		if resp := ask(t, front, http.MethodGet, "/", ""); resp.StatusCode != http.StatusBadGateway {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
	})
}

func TestGuardFromEnv(t *testing.T) {
	t.Run("takes the secret out of the environment the app is handed", func(t *testing.T) {
		guard, kept := GuardFromEnv([]string{
			"PORT=8080",
			OriginSecretVar + "=s3cr3t",
			"NODE_ENV=production",
		})

		if slices.ContainsFunc(kept, func(e string) bool { return strings.HasPrefix(e, OriginSecretVar+"=") }) {
			t.Errorf("the app's environment = %q, which still carries the origin secret", kept)
		}
		if !slices.Equal(kept, []string{"PORT=8080", "NODE_ENV=production"}) {
			t.Errorf("the app's environment = %q, want everything else left as it was", kept)
		}
		if guard == nil {
			t.Fatal("GuardFromEnv built no guard from a deployment that was given a secret")
		}

		admitted := httptest.NewRequest(http.MethodGet, "/", nil)
		admitted.Header.Set(OriginSecretHeader, "s3cr3t")
		if !guard.Admits(admitted) {
			t.Error("the guard turns away the secret it was built from")
		}
		if guard.Admits(httptest.NewRequest(http.MethodGet, "/", nil)) {
			t.Error("the guard admits a request carrying nothing")
		}
	})

	t.Run("builds no guard for a deployment nothing fronts", func(t *testing.T) {
		guard, kept := GuardFromEnv([]string{"PORT=8080"})

		if guard != nil {
			t.Error("GuardFromEnv built a guard for a deployment that was given no secret")
		}
		if !slices.Equal(kept, []string{"PORT=8080"}) {
			t.Errorf("the app's environment = %q, want it untouched", kept)
		}
		if !guard.Admits(httptest.NewRequest(http.MethodGet, "/", nil)) {
			t.Error("a deployment with no secret turns callers away")
		}
	})

	t.Run("an origin secret set to nothing fails closed", func(t *testing.T) {
		guard, _ := GuardFromEnv([]string{OriginSecretVar + "="})

		if guard == nil {
			t.Fatal("an origin secret that was set to nothing left the app open to anyone who finds it")
		}
		if guard.Admits(httptest.NewRequest(http.MethodGet, "/", nil)) {
			t.Error("the guard admits a request carrying nothing")
		}
	})
}
