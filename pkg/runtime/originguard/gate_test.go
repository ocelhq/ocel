package originguard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/runtime/mountedgate"
)

func gatedFront(t *testing.T, up *upstream, contents func() ([]byte, error), now func() time.Time) *httptest.Server {
	t.Helper()
	target, err := url.Parse(up.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(Handler(Options{
		Upstream:   target,
		HealthPath: healthPath,
		Gate:       mountedgate.New(contents, now),
		Now:        now,
	}))
	t.Cleanup(front.Close)
	return front
}

func mountedGate(held *gate.Gate) func() ([]byte, error) {
	return func() ([]byte, error) { return gate.EncodeMounted(held) }
}

func TestAGatedFrontAnswersEveryConformanceVector(t *testing.T) {
	edgeconformance.RunGateWith(t, func(t *testing.T, held gate.Gate, req gate.Request, now time.Time) gate.Verdict {
		if strings.ContainsFunc(req.Target, func(r rune) bool { return r > unicode.MaxASCII }) {
			t.Skip("Go's HTTP client percent-encodes a non-ASCII target before it is sent, so this vector cannot reach the front as written")
		}
		up := serveUpstream(t)
		front := gatedFront(t, up, mountedGate(&held), func() time.Time { return now })
		out, err := http.NewRequest(req.Method, front.URL+req.Target, nil)
		if err != nil {
			t.Fatal(err)
		}
		out.Host = req.Host
		for name, values := range req.Header {
			for _, value := range values {
				out.Header.Add(name, value)
			}
		}
		client := front.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Do(out)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if req.Method == http.MethodHead {
			out.Method = http.MethodGet
			answered, err := client.Do(out)
			if err != nil {
				t.Fatal(err)
			}
			defer answered.Body.Close()
			body, _ = io.ReadAll(answered.Body)
		}
		if up.seen > 0 {
			return gate.Verdict{Forward: gate.Header(up.heard), ResponseHeader: gate.Header(resp.Header)}
		}
		return gate.Verdict{Response: &gate.Response{Status: resp.StatusCode, Header: gate.Header(resp.Header), Body: string(body)}}
	})
}

func TestAGatedFrontReadsARotatedGateWithoutARestart(t *testing.T) {
	up := serveUpstream(t)
	first := gate.Gate{Key: "first", PasswordHash: gate.HashPassword("pw")}
	rotated := gate.Gate{Key: "rotated", PasswordHash: gate.HashPassword("pw")}
	current := &first
	clock := time.Unix(1700000000, 0)
	front := gatedFront(t, up, func() ([]byte, error) { return gate.EncodeMounted(current) }, func() time.Time { return clock })
	session := func(held gate.Gate) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, front.URL+"/page", nil)
		req.Header.Set("Cookie", gate.CookieName+"="+held.SignSession(req.Host, clock))
		resp, err := front.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	if resp := session(first); resp.StatusCode != http.StatusOK {
		t.Fatalf("a session of the published gate = %d, want 200", resp.StatusCode)
	}
	current = &rotated
	clock = clock.Add(mountedgate.CacheFor)
	if resp := session(first); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a session of the rotated-out gate = %d, want 401 once the new gate is read", resp.StatusCode)
	}
}

func TestAGatedFrontServesNoOneWhenItsGateCannotBeRead(t *testing.T) {
	up := serveUpstream(t)
	front := gatedFront(t, up, func() ([]byte, error) { return []byte("{"), nil }, time.Now)

	resp := ask(t, front, http.MethodGet, "/page", "")

	if resp.StatusCode != http.StatusServiceUnavailable || up.seen != 0 {
		t.Errorf("answer = %d, reached the app %d times, want 503 and never the app", resp.StatusCode, up.seen)
	}
	if got := resp.Header.Get("Cache-Control"); got != gate.CacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, gate.CacheControl)
	}
}

func TestAGatedFrontWhoseGateWasWithdrawnServesEveryone(t *testing.T) {
	up := serveUpstream(t)
	front := gatedFront(t, up, mountedGate(nil), time.Now)

	resp := ask(t, front, http.MethodGet, "/page", "")

	if resp.StatusCode != http.StatusOK || up.seen != 1 {
		t.Errorf("answer = %d, reached the app %d times, want the app's 200", resp.StatusCode, up.seen)
	}
}

func TestAGatedFrontTakesThePasswordOffWhatItAdmits(t *testing.T) {
	up := serveUpstream(t)
	held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("pw")}
	front := gatedFront(t, up, mountedGate(&held), time.Now)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/page", nil)
	req.SetBasicAuth("anyone", "pw")
	req.Header.Set("Cookie", "theme=dark")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || up.seen != 1 {
		t.Fatalf("answer = %d, reached the app %d times, want 200 from the app", resp.StatusCode, up.seen)
	}
	if got := up.heard.Get("Authorization"); got != "" {
		t.Errorf("the app was handed Authorization %q, the preview password", got)
	}
	if got := up.heard.Get("Cookie"); got != "theme=dark" {
		t.Errorf("the app heard Cookie %q, want the app's own cookie kept", got)
	}
	if got := resp.Header.Get("X-Robots-Tag"); got != gate.RobotsTag {
		t.Errorf("X-Robots-Tag = %q, want %q on an admitted preview", got, gate.RobotsTag)
	}
}

func TestAGatedFrontRefusesAWebsocketUpgradeWithNoCredentials(t *testing.T) {
	up := serveUpstream(t)
	held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("pw")}
	front := gatedFront(t, up, mountedGate(&held), time.Now)
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/socket", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized || up.seen != 0 {
		t.Errorf("answer = %d, reached the app %d times, want 401 and never the app", resp.StatusCode, up.seen)
	}
}

const probeToken = "probe-token"

func probedFront(t *testing.T, up *upstream, health string, contents func() ([]byte, error)) *httptest.Server {
	t.Helper()
	front := httptest.NewServer(Handler(Options{
		Upstream:   mustParse(t, up.server.URL),
		HealthPath: health,
		ProbeToken: probeToken,
		Gate:       mountedgate.New(contents, time.Now),
	}))
	t.Cleanup(front.Close)
	return front
}

func askAsProbe(t *testing.T, front *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, front.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(ProbeTokenHeader, token)
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAGatedFrontAnswersAProbeCarryingItsTokenWithTheAppsStatusAlone(t *testing.T) {
	up := serveUpstream(t)
	held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("pw")}
	front := probedFront(t, up, healthPath, mountedGate(&held))

	resp := askAsProbe(t, front, healthPath, probeToken)
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || up.seen != 1 {
		t.Fatalf("answer = %d, reached the app %d times, want the app's 200: Cloud Run's probe carries no credentials", resp.StatusCode, up.seen)
	}
	if strings.TrimSpace(string(body)) != "" {
		t.Errorf("a probe read %q, and the health path may be any page of the preview", body)
	}
	if got := up.heard.Get(ProbeTokenHeader); got != "" {
		t.Errorf("the app heard the probe token %q", got)
	}
}

func TestAGatedFrontRefusesAnUnauthenticatedRequestForTheHealthPath(t *testing.T) {
	for name, token := range map[string]string{"no token": "", "a wrong token": "guessed"} {
		t.Run(name, func(t *testing.T) {
			up := serveUpstream(t)
			held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("pw")}
			front := probedFront(t, up, "/", mountedGate(&held))

			resp := askAsProbe(t, front, "/", token)

			if resp.StatusCode != http.StatusUnauthorized || up.seen != 0 {
				t.Errorf("answer = %d, reached the app %d times, want 401 and never the app: the health path is a page of the preview", resp.StatusCode, up.seen)
			}
		})
	}
}

func TestAGatedFrontWithNoProbeTokenExemptsNoPath(t *testing.T) {
	up := serveUpstream(t)
	held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("pw")}
	front := httptest.NewServer(Handler(Options{
		Upstream:   mustParse(t, up.server.URL),
		HealthPath: "/",
		Gate:       mountedgate.New(mountedGate(&held), time.Now),
	}))
	t.Cleanup(front.Close)

	resp := askAsProbe(t, front, "/", "")

	if resp.StatusCode != http.StatusUnauthorized || up.seen != 0 {
		t.Errorf("answer = %d, reached the app %d times, want 401 and never the app", resp.StatusCode, up.seen)
	}
}

func TestAGatedFrontWhoseGateCannotBeReadAnswersOnlyItsProbe(t *testing.T) {
	up := serveUpstream(t)
	front := probedFront(t, up, "/", func() ([]byte, error) { return []byte("{"), nil })

	if resp := askAsProbe(t, front, "/", ""); resp.StatusCode != http.StatusServiceUnavailable || up.seen != 0 {
		t.Errorf("unauthenticated answer = %d, reached the app %d times, want 503 and never the app", resp.StatusCode, up.seen)
	}
	if resp := askAsProbe(t, front, "/", probeToken); resp.StatusCode != http.StatusOK || up.seen != 1 {
		t.Errorf("probe answer = %d, reached the app %d times, want the app's status", resp.StatusCode, up.seen)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
