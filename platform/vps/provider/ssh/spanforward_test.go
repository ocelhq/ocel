package ssh

import (
	"context"
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

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const collectorToken = "s3cret-collector-token"

type collected struct {
	mu     sync.Mutex
	bodies []string
}

func (c *collected) received() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.bodies...)
}

func collectorGuarded(t *testing.T) *collected {
	t.Helper()
	c := &collected{}
	server := httptest.NewUnstartedServer(nil)
	address := server.Listener.Addr().String()
	server.Config.Handler = localrpc.LoopbackGuard(address, collectorToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
			http.Error(w, "not the trace path", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.bodies = append(c.bodies, string(body))
		c.mu.Unlock()
	}))
	server.Start()
	t.Cleanup(server.Close)
	previous := tracing.ReadCollector()
	tracing.SetCollector(tracing.Collector{URL: "http://" + address + "/v1/traces", Token: collectorToken})
	t.Cleanup(func() { tracing.SetCollector(previous) })
	return c
}

func multiplexedStandIn(t *testing.T) (*Session, string) {
	t.Helper()
	session := sshStandingIn(t)
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	session.control = filepath.Join(dir, "1-1")
	log := filepath.Join(t.TempDir(), "said")
	t.Setenv(standInLogEnv, log)
	t.Setenv(standInRunEnv, "1")
	return session, log
}

func said(t *testing.T, log string) string {
	t.Helper()
	raw, _ := os.ReadFile(log)
	return string(raw)
}

func forwardedTo(t *testing.T, log, remote string) string {
	t.Helper()
	for line := range strings.Lines(said(t, log)) {
		if _, spec, ok := strings.Cut(strings.TrimSpace(line), "-O forward -R "+remote+":"); ok {
			local, _, _ := strings.Cut(spec, " ")
			return local
		}
	}
	t.Fatalf("ssh never asked the master to forward %s:\n%s", remote, said(t, log))
	return ""
}

func postOver(t *testing.T, socket, body string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := client.Post("http://ocel-spans/v1/traces", "application/x-protobuf", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post over %s: %v", socket, err)
	}
	_ = resp.Body.Close()
	return resp
}

func TestASessionForwardsAPrivateBoxSocketToTheCollectorAndAddsTheTokenTheBoxNeverHolds(t *testing.T) {
	collector := collectorGuarded(t)
	session, log := multiplexedStandIn(t)

	remote := session.ForwardSpans()
	if remote == "" {
		t.Fatalf("ForwardSpans() = \"\", want a socket on the box:\n%s", said(t, log))
	}
	dir := filepath.Dir(remote)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("the socket's directory %s is %v, %v, want a directory only the login can enter", dir, info, err)
	}
	local := forwardedTo(t, log, remote)
	if again := session.ForwardSpans(); again != remote {
		t.Errorf("a second ForwardSpans() = %q, want the session's one forward %q", again, remote)
	}

	if resp := postOver(t, local, "spans"); resp.StatusCode != http.StatusOK {
		t.Fatalf("an export over the forward was answered %s, want the collector to take it with the token added", resp.Status)
	}
	if got := collector.received(); len(got) != 1 || got[0] != "spans" {
		t.Errorf("the collector received %q, want the export", got)
	}
	if strings.Contains(said(t, log), collectorToken) {
		t.Errorf("ssh ran with the collector's token in its argv:\n%s", said(t, log))
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if !strings.Contains(said(t, log), "-O cancel -R "+remote+":"+local) {
		t.Errorf("closing the session never cancelled the forward:\n%s", said(t, log))
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Errorf("%s outlived the session", dir)
	}
	if _, err := os.Lstat(local); err == nil {
		t.Errorf("the provider's end %s outlived the session", local)
	}
}

func TestTheProviderEndOfTheForwardTakesOnlyAnExportOfSpans(t *testing.T) {
	collector := collectorGuarded(t)
	session, log := multiplexedStandIn(t)
	defer session.Close()

	local := forwardedTo(t, log, session.ForwardSpans())
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", local)
		},
	}}
	for _, asked := range []struct{ method, path string }{{http.MethodGet, "/v1/traces"}, {http.MethodPost, "/v1/metrics"}, {http.MethodPost, "/"}} {
		req, _ := http.NewRequest(asked.method, "http://ocel-spans"+asked.path, strings.NewReader("x"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s %s over the forward was answered %s, want it refused", asked.method, asked.path, resp.Status)
		}
	}
	if got := collector.received(); len(got) != 0 {
		t.Errorf("the collector received %q, want nothing but an export of spans passed on with the token", got)
	}
}

func TestASessionWithNoCollectorForwardsNoSpans(t *testing.T) {
	previous := tracing.ReadCollector()
	tracing.SetCollector(tracing.Collector{})
	t.Cleanup(func() { tracing.SetCollector(previous) })
	session, log := multiplexedStandIn(t)

	if remote := session.ForwardSpans(); remote != "" {
		t.Errorf("ForwardSpans() = %q with no collector, want none", remote)
	}
	if ran := said(t, log); ran != "" {
		t.Errorf("ssh ran %q with no collector to forward to, want nothing run", ran)
	}
}

func TestABoxThatRefusesTheForwardIsLeftWithNoSocketAndTheSessionUntraced(t *testing.T) {
	collectorGuarded(t)
	session, log := multiplexedStandIn(t)
	t.Setenv(standInOpEnv, "forward")

	if remote := session.ForwardSpans(); remote != "" {
		t.Errorf("ForwardSpans() = %q from a box that refused it, want none", remote)
	}
	for line := range strings.Lines(said(t, log)) {
		if _, remote, ok := strings.Cut(strings.TrimSpace(line), "-O forward -R "); ok {
			dir := filepath.Dir(strings.SplitN(remote, ":", 2)[0])
			if _, err := os.Lstat(dir); err == nil {
				t.Errorf("%s is left on the box after the forward was refused", dir)
			}
		}
	}
	if err := session.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}
