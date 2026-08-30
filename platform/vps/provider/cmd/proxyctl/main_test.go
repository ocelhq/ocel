package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type admin struct {
	mu     sync.Mutex
	seen   []string
	loaded []byte
	status int
	body   string
}

func served(t *testing.T) (*admin, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "admin.sock")
	if len(socket) > 100 {
		t.Skipf("a unix socket path this host accepts does not fit under %s", socket)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	held := &admin{status: http.StatusOK, body: "[]"}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, _ := io.ReadAll(r.Body)
		held.mu.Lock()
		held.seen = append(held.seen, r.Method+" "+r.URL.Path)
		if len(read) > 0 {
			held.loaded = read
		}
		status, body := held.status, held.body
		held.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv(socketEnv, socket)
	return held, socket
}

func (a *admin) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

func ran(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errs strings.Builder
	return run(argv, &out, &errs), out.String(), errs.String()
}

func configFile(t *testing.T, socket, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caddy.json")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "SOCKET", socket)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheDrainReadAsksTheOneEndpointThatCountsWhatIsStillInFlight(t *testing.T) {
	held, _ := served(t)
	held.body = `[{"address":"old:3000","num_requests":1}]`

	code, out, errs := ran(t, "upstreams")
	if code != 0 {
		t.Fatalf("upstreams = %d, %q", code, errs)
	}
	if asked := held.asked(); len(asked) != 1 || asked[0] != "GET "+upstreamsPath {
		t.Fatalf("the drain read asked %v, want GET %s: it is the one endpoint reporting how many requests an upstream still holds, and the metrics do not mirror it",
			asked, upstreamsPath)
	}
	if !strings.Contains(out, "num_requests") {
		t.Errorf("the drain read printed %q, and the release loop polls the count in it", out)
	}
}

func TestTheDrainReadIsAVerbTheHelperCarriesRatherThanOneTheCallerSpells(t *testing.T) {
	_, _ = served(t)

	if code, _, errs := ran(t, "drain"); code == 0 {
		t.Fatal("the helper answered a verb it does not carry")
	} else if !strings.Contains(errs, "upstreams") {
		t.Errorf("the helper's usage is %q and never names the drain read: a verb nothing names is one a contributor deletes", errs)
	}
}

func TestTheGateCallsATargetUpOnlyOnATwoHundred(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/up" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer backend.Close()
	target := strings.TrimPrefix(backend.URL, "http://")

	if code, out, errs := ran(t, "gate", target, "/up", "5"); code != 0 {
		t.Errorf("gating a target answering 204 = %d, %q %q: up means a 2xx on the health path", code, out, errs)
	} else if strings.TrimSpace(out) != "204" {
		t.Errorf("the gate printed %q, want the status it read", out)
	}
	if code, out, _ := ran(t, "gate", target, "/down", "5"); code != exitUnhealthy {
		t.Errorf("gating a target answering 502 = %d, want %d: forcing past the gate is rejected", code, exitUnhealthy)
	} else if strings.TrimSpace(out) != "502" {
		t.Errorf("the gate printed %q, and answered-with-N is a different bug from never-answered", out)
	}
	if code, _, errs := ran(t, "gate", "127.0.0.1:1", "/up", "1"); code != exitSilent {
		t.Errorf("gating a target that answers nothing = %d, want %d: %q", code, exitSilent, errs)
	}
}

func TestTheFlipRefusesEveryConfigThatWouldMoveTheAdminEndpoint(t *testing.T) {
	held, socket := served(t)

	for what, document := range map[string]string{
		"no admin block at all":                           `{"apps":{"http":{"servers":{}}}}`,
		"an admin block disabled":                         `{"admin":{"disabled":true,"listen":"unix/SOCKET"}}`,
		"a tcp listener":                                  `{"admin":{"listen":"127.0.0.1:2019"}}`,
		"another socket":                                  `{"admin":{"listen":"unix//run/other.sock"}}`,
		"the socket named inside a comment-shaped string": `{"apps":{"note":"SOCKET"}}`,
	} {
		code, _, errs := ran(t, "flip", configFile(t, socket, document))
		if code == 0 {
			t.Errorf("the flip loaded a config carrying %s, and caddy applies the admin section before it validates the rest", what)
		}
		if !strings.Contains(errs, socket) {
			t.Errorf("the flip refused a config carrying %s with %q, want it to name the socket the config must keep", what, errs)
		}
	}
	if asked := held.asked(); len(asked) != 0 {
		t.Errorf("a refused flip still reached the proxy with %v", asked)
	}
}

func TestTheFlipLoadsAConfigThatKeepsTheSocketAndCarriesItsPermissionSuffix(t *testing.T) {
	held, socket := served(t)
	document := `{"admin":{"listen":"unix/SOCKET|0600"},"apps":{"http":{"servers":{}}}}`
	path := configFile(t, socket, document)

	if code, _, errs := ran(t, "flip", path); code != 0 {
		t.Fatalf("flip = %d, %q: the socket carries an optional mode after a pipe and the address before it is what must match", code, errs)
	}
	if asked := held.asked(); len(asked) != 1 || asked[0] != "POST "+loadPath {
		t.Fatalf("the flip asked %v, want POST %s: it blocks until the reload completes or fails", asked, loadPath)
	}
	var loaded map[string]any
	if err := json.Unmarshal(held.loaded, &loaded); err != nil {
		t.Fatalf("the flip posted something that is not the config it was handed: %v", err)
	}
	if loaded["admin"] == nil {
		t.Error("the flip posted a config carrying no admin block, and what it reads is what it must send")
	}
}

func TestAProxyThatRefusesTheConfigIsAFailedFlipRatherThanASilentOne(t *testing.T) {
	held, socket := served(t)
	held.status, held.body = http.StatusBadRequest, "loading config: unknown module"

	code, _, errs := ran(t, "flip", configFile(t, socket, `{"admin":{"listen":"unix/SOCKET"}}`))
	if code == 0 {
		t.Fatal("a config the proxy rejected exited zero, and the release loop treats a non-zero exit as the one condition it branches on")
	}
	if !strings.Contains(errs, "unknown module") {
		t.Errorf("the flip failed with %q, and caddy's error names the offending field", errs)
	}
}

func TestTheHelperReadsTheServedConfigOverTheSameSocketAndNoOtherPath(t *testing.T) {
	held, _ := served(t)
	held.body = `"30s"`

	code, out, errs := ran(t, "config", "apps/http/grace_period")
	if code != 0 {
		t.Fatalf("config = %d, %q", code, errs)
	}
	if asked := held.asked(); len(asked) != 1 || asked[0] != "GET "+configPath+"apps/http/grace_period" {
		t.Fatalf("reading the served config asked %v, want it under %s", asked, configPath)
	}
	if strings.TrimSpace(out) != `"30s"` {
		t.Errorf("the helper printed %q, and what the proxy serves is what it must print", out)
	}

	t.Setenv(socketEnv, filepath.Join(t.TempDir(), "gone.sock"))
	if code, _, errs := ran(t, "upstreams"); code == 0 {
		t.Error("the helper answered with no socket to speak over, so what it read came from somewhere else")
	} else if !strings.Contains(errs, "gone.sock") {
		t.Errorf("the helper failed with %q, want it to name the socket it could not reach", errs)
	}
}
