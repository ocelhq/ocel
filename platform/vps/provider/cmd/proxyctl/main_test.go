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
	"time"
)

type admin struct {
	mu     sync.Mutex
	seen   []string
	loaded []byte
	status int
	body   string
	queue  []string
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
		if len(held.queue) > 0 {
			body = held.queue[0]
			if len(held.queue) > 1 {
				held.queue = held.queue[1:]
			}
		}
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
	if code, out, _ := ran(t, "gate", target, "/down", "1"); code != exitUnhealthy {
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

func refusing(t *testing.T, refusals int) string {
	t.Helper()
	var mu sync.Mutex
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		left := refusals
		refusals--
		mu.Unlock()
		if left > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	return strings.TrimPrefix(backend.URL, "http://")
}

func TestTheGateKeepsAskingUntilTheWindowCloses(t *testing.T) {
	target := refusing(t, 3)

	code, out, errs := ran(t, "gate", target, "/up", "10")
	if code != 0 {
		t.Fatalf("gating a target that warms up over its first answers = %d, %q %q: a container still binding its port refuses in milliseconds and the window is what it is given",
			code, out, errs)
	}
	if strings.TrimSpace(out) != "200" {
		t.Errorf("the gate printed %q, want the status it finally read", out)
	}
}

func TestAGateThatExpiresSaysWhetherTheTargetAnsweredAtAllAndDoesNotConflateTheTwo(t *testing.T) {
	answering := refusing(t, 1<<30)

	code, out, said := ran(t, "gate", answering, "/up", "1")
	if code != exitUnhealthy {
		t.Fatalf("gating a target that answers 503 throughout = %d, want %d: %q", code, exitUnhealthy, said)
	}
	if strings.TrimSpace(out) != "503" {
		t.Errorf("the gate printed %q, want the last status it read", out)
	}

	silentCode, _, silent := ran(t, "gate", "127.0.0.1:1", "/up", "1")
	if silentCode != exitSilent {
		t.Fatalf("gating a target that answers nothing = %d, want %d", silentCode, exitSilent)
	}
	if said == silent {
		t.Errorf("a target that answered %s and one that answered nothing both said %q, and they are different bugs with different fixes", "503", said)
	}
	if !strings.Contains(said, "503") {
		t.Errorf("the expired gate said %q and never named the status it kept reading", said)
	}
}

func TestTheDrainReturnsTheMomentTheRetiredUpstreamReportsNothingInFlight(t *testing.T) {
	held, socket := served(t)
	held.queue = []string{
		`[{"address":"old:8080","num_requests":1},{"address":"new:8080","num_requests":0}]`,
		`[{"address":"old:8080","num_requests":1},{"address":"new:8080","num_requests":0}]`,
		`[{"address":"old:8080","num_requests":0},{"address":"new:8080","num_requests":1}]`,
	}

	var out, errs strings.Builder
	if code := draining(socket, "old:8080", 30*time.Second, &out, &errs); code != 0 {
		t.Fatalf("draining an upstream that finishes its request = %d, %q", code, errs.String())
	}
	if asked := held.asked(); len(asked) != 3 {
		t.Errorf("the drain asked %d times, want it to stop at the poll that read zero: %v", len(asked), asked)
	}
	if out.String() != "" {
		t.Errorf("a drain that completed printed %q, and the expiry line is what an operator is warned by", out.String())
	}
}

func TestADrainThatExpiresIsNotAFailureAndCarriesTheCountStillInFlight(t *testing.T) {
	held, socket := served(t)
	held.body = `[{"address":"old:8080","num_requests":3}]`

	var out, errs strings.Builder
	if code := draining(socket, "old:8080", time.Second, &out, &errs); code != 0 {
		t.Fatalf("draining past the window = %d, want it borne as a warning: the new release is serving", code)
	}
	printed := strings.TrimSpace(out.String())
	if !strings.Contains(printed, "old:8080") || !strings.Contains(printed, "3") {
		t.Errorf("the expired drain printed %q, want the address and the count still in flight", printed)
	}
	if len(held.asked()) < 2 {
		t.Errorf("the drain asked %v, want it polled across the window rather than sampled once", held.asked())
	}
}

func TestARetiredUpstreamAbsentFromThePoolIsABrokenCompositionRatherThanADrain(t *testing.T) {
	_, socket := served(t)

	var out, errs strings.Builder
	code := draining(socket, "old:8080", time.Second, &out, &errs)
	if code == 0 {
		t.Fatal("an upstream the proxy never reported read as drained, and a proxy upgrade that drops a retired upstream from the pool would stop old containers under live requests forever")
	}
	if code != exitUnattributable {
		t.Errorf("the drain exited %d, want %d: it is neither a gate failure nor a rejected config", code, exitUnattributable)
	}
	if !strings.Contains(errs.String(), "old:8080") {
		t.Errorf("the drain failed with %q, want it to name the address the pool never carried", errs.String())
	}
}

func loads(asked []string) int {
	count := 0
	for _, one := range asked {
		if one == "POST "+loadPath {
			count++
		}
	}
	return count
}

func TestTheComposedCallGatesBeforeItEverReachesTheProxy(t *testing.T) {
	held, socket := served(t)
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET"}}`)

	code, _, errs := ran(t, "deploy", "--target", "127.0.0.1:1", "--health-check-path", "/up",
		"--deploy-timeout", "1", "--drain-timeout", "1", "--config", config, "--retire", "old:8080")
	if code != exitSilent {
		t.Fatalf("deploying against a target that never answered = %d, want %d: %q", code, exitSilent, errs)
	}
	if asked := held.asked(); len(asked) != 0 {
		t.Errorf("a failed gate still reached the proxy with %v, and no flip is attempted when the gate does not pass", asked)
	}
}

func TestAConfigTheProxyRejectsFailsTheComposedCallWithWhatTheProxySaid(t *testing.T) {
	held, socket := served(t)
	held.status, held.body = http.StatusBadRequest, "loading config: unknown module http.handlers.nonsense"
	target := refusing(t, 0)
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET"}}`)

	code, _, errs := ran(t, "deploy", "--target", target, "--health-check-path", "/up",
		"--deploy-timeout", "5", "--drain-timeout", "1", "--config", config, "--retire", "old:8080")
	if code != exitRefused {
		t.Fatalf("deploying a config the proxy rejected = %d, want %d", code, exitRefused)
	}
	if !strings.Contains(errs, "http.handlers.nonsense") {
		t.Errorf("the composed call failed with %q, and caddy's 400 names the offending field", errs)
	}
	if asked := held.asked(); loads(asked) != 1 || len(asked) != 1 {
		t.Errorf("the composed call asked %v, want the load and nothing after it: a rejected config drains nothing", asked)
	}
}

func TestOneCallGatesFlipsAndDrainsAndItsExitCodeIsTheWholeAnswer(t *testing.T) {
	held, socket := served(t)
	held.queue = []string{
		`[{"address":"old:8080","num_requests":1}]`,
		`[{"address":"old:8080","num_requests":0}]`,
	}
	target := refusing(t, 2)
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET"}}`)

	code, out, errs := ran(t, "deploy", "--target", target, "--health-check-path", "/up",
		"--deploy-timeout", "10", "--drain-timeout", "10", "--config", config, "--retire", "old:8080")
	if code != 0 {
		t.Fatalf("deploy = %d, %q %q", code, out, errs)
	}
	asked := held.asked()
	if len(asked) < 2 || asked[0] != "POST "+loadPath {
		t.Fatalf("the composed call asked %v, want the flip first: the drain reads the pool the flip installed", asked)
	}
	if loads(asked) != 1 {
		t.Errorf("the composed call posted %d configs, want exactly one", loads(asked))
	}
	for _, after := range asked[1:] {
		if after != "GET "+upstreamsPath {
			t.Errorf("the composed call asked %q after the flip, want nothing but the drain read", after)
		}
	}
}

func TestAFirstDeployWithNothingToRetireDrainsNothingAtAll(t *testing.T) {
	held, socket := served(t)
	target := refusing(t, 0)
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET"}}`)

	code, _, errs := ran(t, "deploy", "--target", target, "--health-check-path", "/up",
		"--deploy-timeout", "5", "--drain-timeout", "5", "--config", config)
	if code != 0 {
		t.Fatalf("deploying with nothing to retire = %d, %q", code, errs)
	}
	if asked := held.asked(); len(asked) != 1 || asked[0] != "POST "+loadPath {
		t.Errorf("a first deploy asked %v, want the flip alone: there is no retired upstream to count", asked)
	}
}
