package main

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
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

	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
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

func gated(target, path string, window time.Duration) (int, string, string) {
	var out, errs strings.Builder
	return gating(target, path, window, &out, &errs), out.String(), errs.String()
}

func ran(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	return ranIn(t, t.TempDir(), argv...)
}

func ranIn(t *testing.T, data string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errs strings.Builder
	return run(data, argv, &out, &errs), out.String(), errs.String()
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

	for _, verb := range []string{"drain", "gate"} {
		code, _, errs := ran(t, verb, "127.0.0.1:1", "/up", "1")
		if code == 0 {
			t.Errorf("the helper answered %q, a verb no host code spells", verb)
		}
		if strings.Contains(errs, verb+" <") {
			t.Errorf("the helper's usage still offers %q: %q", verb, errs)
		}
		if !strings.Contains(errs, "upstreams") {
			t.Errorf("the helper's usage is %q and never names the drain read: a verb nothing names is one a contributor deletes", errs)
		}
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

	if code, out, errs := gated(target, "/up", 5*time.Second); code != 0 {
		t.Errorf("gating a target answering 204 = %d, %q %q: up means a 2xx on the health path", code, out, errs)
	} else if strings.TrimSpace(out) != "204" {
		t.Errorf("the gate printed %q, want the status it read", out)
	}
	if code, out, _ := gated(target, "/down", time.Second); code != exitUnhealthy {
		t.Errorf("gating a target answering 502 = %d, want %d: forcing past the gate is rejected", code, exitUnhealthy)
	} else if strings.TrimSpace(out) != "502" {
		t.Errorf("the gate printed %q, and answered-with-N is a different bug from never-answered", out)
	}
	if code, _, errs := gated("127.0.0.1:1", "/up", time.Second); code != exitSilent {
		t.Errorf("gating a target that answers nothing = %d, want %d: %q", code, exitSilent, errs)
	}
}

func TestTheFlipRefusesEveryConfigThatWouldMoveTheAdminEndpoint(t *testing.T) {
	held, socket := served(t)

	for what, document := range map[string]string{
		"no admin block at all":   `{"apps":{"http":{"servers":{}}}}`,
		"an admin block disabled": `{"admin":{"disabled":true,"listen":"unix/SOCKET"}}`,
		"a tcp listener":          `{"admin":{"listen":"127.0.0.1:2019"}}`,
		"another socket":          `{"admin":{"listen":"unix//run/other.sock|0600"}}`,
		"the socket named without the mode it is created under":      `{"admin":{"listen":"unix/SOCKET"}}`,
		"the socket named under a mode the whole container can dial": `{"admin":{"listen":"unix/SOCKET|0666"}}`,
		"the socket named inside a comment-shaped string":            `{"apps":{"note":"SOCKET"}}`,
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

func TestTheFlipLoadsAConfigThatKeepsTheSocketUnderTheModeItIsCreatedWith(t *testing.T) {
	held, socket := served(t)
	document := `{"admin":{"listen":"` + caddyadmin.Listen(socket) + `"},"apps":{"http":{"servers":{}}}}`
	path := configFile(t, socket, document)

	if code, _, errs := ran(t, "flip", path); code != 0 {
		t.Fatalf("flip = %d, %q: the address the helper itself declares is the one config it must always load", code, errs)
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

	code, _, errs := ran(t, "flip", configFile(t, socket, `{"admin":{"listen":"unix/SOCKET|0600"}}`))
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

	code, out, errs := gated(target, "/up", 10*time.Second)
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

	code, out, said := gated(answering, "/up", time.Second)
	if code != exitUnhealthy {
		t.Fatalf("gating a target that answers 503 throughout = %d, want %d: %q", code, exitUnhealthy, said)
	}
	if strings.TrimSpace(out) != "503" {
		t.Errorf("the gate printed %q, want the last status it read", out)
	}

	silentCode, _, silent := gated("127.0.0.1:1", "/up", time.Second)
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
	if !strings.HasPrefix(printed, caddyadmin.DrainExpired+" ") {
		t.Errorf("the expired drain printed %q, and the release loop reads that line by the prefix both sides share", printed)
	}
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
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET|0600"}}`)

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
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET|0600"}}`)

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
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET|0600"}}`)

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
	config := configFile(t, socket, `{"admin":{"listen":"unix/SOCKET|0600"}}`)

	code, _, errs := ran(t, "deploy", "--target", target, "--health-check-path", "/up",
		"--deploy-timeout", "5", "--drain-timeout", "5", "--config", config)
	if code != 0 {
		t.Fatalf("deploying with nothing to retire = %d, %q", code, errs)
	}
	if asked := held.asked(); len(asked) != 1 || asked[0] != "POST "+loadPath {
		t.Errorf("a first deploy asked %v, want the flip alone: there is no retired upstream to count", asked)
	}
}

func listening(t *testing.T, answer func(net.Conn)) string {
	t.Helper()

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close() })
	go func() {
		for {
			taken, err := held.Accept()
			if err != nil {
				return
			}
			go answer(taken)
		}
	}()
	return held.Addr().String()
}

func TestTheLeafReadIsTakenOffTheHandshakeAndAsksTheAdminApiNothing(t *testing.T) {
	held, _ := served(t)
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(proxy.Close)

	var out, errs strings.Builder
	if code := serving(proxy.Listener.Addr().String(), "shop.example.com", &out, &errs); code != 0 {
		t.Fatalf("leaf off a proxy that served a certificate = %d: %q", code, errs.String())
	}
	if block, _ := pem.Decode([]byte(out.String())); block == nil || block.Type != "CERTIFICATE" {
		t.Errorf("leaf wrote %q, want the certificate the peer presented on the handshake", out.String())
	}
	if asked := held.asked(); len(asked) != 0 {
		t.Errorf("the leaf read asked the admin api %v, want nothing: caddy exposes no managed-certificate inventory there, and a 200 from a config load is not evidence a certificate exists because caddy obtains them in the background", asked)
	}
}

func TestAHandshakeThatFailedForAnyReasonButAMissingCertificateIsNotReportedAsPending(t *testing.T) {
	t.Parallel()

	declining := listening(t, func(taken net.Conn) {
		spoken := tls.Server(taken, &tls.Config{
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return nil, errors.New("no certificate available for this name yet")
			},
		})
		_ = spoken.Handshake()
		spoken.Close()
	})
	silent := listening(t, func(taken net.Conn) { taken.Close() })

	for what, at := range map[string]struct {
		address string
		want    int
	}{
		"a proxy that has not obtained one for this name": {declining, exitUnhealthy},
		"a peer that never spoke tls at all":              {silent, exitUnservable},
	} {
		var out, errs strings.Builder
		if code := serving(at.address, "shop.example.com", &out, &errs); code != at.want {
			t.Errorf("leaf over %s = %d, want %d: %q", what, code, at.want, errs.String())
		}
	}
}

func stored(t *testing.T, root, issuer, subject string) string {
	t.Helper()

	held := filepath.Join(root, "caddy", "certificates", issuer, subject)
	if err := os.MkdirAll(held, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{subject + ".crt", subject + ".key", subject + ".json"} {
		if err := os.WriteFile(filepath.Join(held, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return held
}

func standing(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Stat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

const liveIssuer = "acme-v02.api.letsencrypt.org-directory"

func TestForgettingAHostnameTakesItsCertificateAndItsKeyOutOfTheProxysStore(t *testing.T) {
	root := t.TempDir()
	going := stored(t, root, liveIssuer, "shop--pr-7--web.preview.example.com")
	staying := stored(t, root, liveIssuer, "shop--pr-9--web.preview.example.com")

	code, out, errs := ranIn(t, root, "forget", "shop--pr-7--web.preview.example.com")
	if code != 0 {
		t.Fatalf("forget = %d: %q", code, errs)
	}
	if standing(t, going) {
		t.Errorf("%s still stands, and the proxy's store grows one certificate and one private key per preview hostname ever served: a teardown that leaves them behind leaves bytes behind", going)
	}
	if !standing(t, staying) {
		t.Errorf("%s went with it, and it belongs to a preview that is still up", staying)
	}
	if !strings.Contains(out, going) {
		t.Errorf("forget wrote %q, want the path it removed: the deploy's reporter says what teardown took", out)
	}
}

func TestForgettingReachesEveryIssuerTheProxyEverOrderedFrom(t *testing.T) {
	root := t.TempDir()
	hostname := "shop--pr-7--web.preview.example.com"
	held := []string{
		stored(t, root, liveIssuer, hostname),
		stored(t, root, "acme-staging-v02.api.letsencrypt.org-directory", hostname),
		stored(t, root, "local", hostname),
	}

	if code, _, errs := ranIn(t, root, "forget", hostname); code != 0 {
		t.Fatalf("forget = %d: %q", code, errs)
	}
	for _, path := range held {
		if standing(t, path) {
			t.Errorf("%s still stands: an issuer that answered once has a directory of its own, and a box that changed issuers holds a pair under each", path)
		}
	}
}

func TestForgettingLeavesTheAcmeAccountKeyAndEveryOtherHostnameWhereTheyStand(t *testing.T) {
	root := t.TempDir()
	account := filepath.Join(root, "caddy", "acme", liveIssuer, "users", "default")
	if err := os.MkdirAll(account, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account, "default.key"), []byte("account"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored(t, root, liveIssuer, "shop--pr-7--web.preview.example.com")

	if code, _, errs := ranIn(t, root, "forget", "shop--pr-7--web.preview.example.com"); code != 0 {
		t.Fatalf("forget = %d: %q", code, errs)
	}
	if !standing(t, filepath.Join(account, "default.key")) {
		t.Errorf("the acme account key went with the certificate, and losing it re-triggers the ca's registered-domain ceiling for every hostname this box serves")
	}
}

func TestForgettingAHostnameNothingWasEverIssuedForRemovesNothingAndRefusesNothing(t *testing.T) {
	root := t.TempDir()
	staying := stored(t, root, liveIssuer, "shop--pr-9--web.preview.example.com")

	code, out, errs := ranIn(t, root, "forget", "shop--pr-7--web.preview.example.com")
	if code != 0 {
		t.Fatalf("forget of a hostname with no certificate = %d: %q; teardown calls this unconditionally and a refusal here breaks it", code, errs)
	}
	if out != "" {
		t.Errorf("forget wrote %q, want nothing: it removed nothing", out)
	}
	if !standing(t, staying) {
		t.Errorf("%s went with a hostname that was never issued for", staying)
	}
}

func TestForgettingAStoreThatWasNeverWrittenToIsANoOp(t *testing.T) {
	root := t.TempDir()

	if code, out, errs := ranIn(t, root, "forget", "shop--pr-7--web.preview.example.com"); code != 0 || out != "" {
		t.Errorf("forget against a proxy that has obtained nothing = %d, %q: %q", code, out, errs)
	}
}

func TestForgettingRefusesANameThatIsAPathRatherThanAHostname(t *testing.T) {
	root := t.TempDir()
	staying := stored(t, root, liveIssuer, "shop--pr-9--web.preview.example.com")

	for _, name := range []string{"", "..", "../acme", "shop/../../acme", "*.preview.example.com"} {
		code, _, errs := ranIn(t, root, "forget", name)
		if code != exitRefused {
			t.Errorf("forget %q = %d, want %d: the argument is spent as a directory name under the proxy's store, so anything but a hostname is a path traversal into the account key beside it", name, code, exitRefused)
		}
		if !strings.Contains(errs, "hostname") {
			t.Errorf("forget %q refused with %q, and it never says what it will accept", name, errs)
		}
	}
	if !standing(t, staying) {
		t.Errorf("a refused forget still removed %s", staying)
	}
}

func TestForgettingTakesEveryHostnameOnePreviewClaimedInOneCall(t *testing.T) {
	root := t.TempDir()
	going := []string{
		stored(t, root, liveIssuer, "shop--pr-7--api.preview.example.com"),
		stored(t, root, liveIssuer, "shop--pr-7--web.preview.example.com"),
	}
	staying := stored(t, root, liveIssuer, "shop--pr-9--web.preview.example.com")

	if code, _, errs := ranIn(t, root, "forget",
		"shop--pr-7--api.preview.example.com", "shop--pr-7--web.preview.example.com"); code != 0 {
		t.Fatalf("forget = %d: %q", code, errs)
	}
	for _, path := range going {
		if standing(t, path) {
			t.Errorf("%s still stands: a preview is one hostname per app, and its teardown is one call rather than one per app", path)
		}
	}
	if !standing(t, staying) {
		t.Error("the other branch's hostname went with them")
	}
}

func TestForgettingRefusesAStoreItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode says")
	}
	root := t.TempDir()
	certificates := filepath.Join(root, "caddy", "certificates")
	if err := os.MkdirAll(certificates, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(certificates, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(certificates, 0o700) })

	code, out, errs := ranIn(t, root, "forget", "shop--pr-7--web.preview.example.com")
	if code != exitRefused {
		t.Errorf("forget over a store it cannot read = %d, want %d: a store that answers neither its contents nor \"nothing here\" is not a teardown that removed anything, and reporting success over it leaves the pairs standing", code, exitRefused)
	}
	if out != "" {
		t.Errorf("forget wrote %q, want nothing: it removed nothing", out)
	}
	if !strings.Contains(errs, "ocel-proxyctl") {
		t.Errorf("forget refused with %q, and the deploy prints this line as the whole of why", errs)
	}
}

func TestForgettingRefusesAPairItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root unlinks inside a directory whatever its mode says")
	}
	root := t.TempDir()
	const hostname = "shop--pr-7--web.preview.example.com"
	stored(t, root, liveIssuer, hostname)
	issuer := filepath.Join(root, "caddy", "certificates", liveIssuer)
	if err := os.Chmod(issuer, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(issuer, 0o700) })

	code, out, errs := ranIn(t, root, "forget", hostname)
	if code != exitRefused {
		t.Errorf("forget of a pair it cannot unlink = %d, want %d: the bytes are still there, and a teardown that says otherwise is the one term that keeps growing", code, exitRefused)
	}
	if out != "" {
		t.Errorf("forget wrote %q, want nothing: it removed nothing, and the reporter prints this as what teardown took", out)
	}
	if !strings.Contains(errs, "ocel-proxyctl") {
		t.Errorf("forget refused with %q, and the deploy prints this line as the whole of why", errs)
	}
}

func TestForgettingRefusesTheSharedPreviewWildcardsOwnDirectory(t *testing.T) {
	root := t.TempDir()
	staying := stored(t, root, liveIssuer, "wildcard_.preview.example.com")

	code, _, errs := ranIn(t, root, "forget", "wildcard_.preview.example.com")
	if code != exitRefused {
		t.Errorf("forget %q = %d, want %d: that is the directory the proxy writes a wildcard under, and it is the one certificate answering every preview this box serves", "wildcard_.preview.example.com", code, exitRefused)
	}
	if !strings.Contains(errs, "hostname") {
		t.Errorf("forget refused with %q, and it never says what it will accept", errs)
	}
	if !standing(t, staying) {
		t.Error("a refused forget still took the shared preview wildcard's pair")
	}
}
