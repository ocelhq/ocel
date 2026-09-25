package switchboard_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func TestTheTableAdmitsEveryNameTheBoxAnswersWithACertificateAndNoOther(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[
			{"owner":"ocel--shop--production","hostname":"Shop.example.com","pointer":"@production"},
			{"owner":"ocel--blog--production","hostname":"blog.example.com","pointer":"@production"}],
		"routes":[{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"shop-web-1:3000"}],
		"preview":"preview.example.com",
		"connector":"box.example.com"}`)

	for what, hostname := range map[string]string{
		"a claimed hostname":                         "shop.example.com",
		"a claimed hostname spelled in another case": "SHOP.EXAMPLE.COM",
		"a hostname claimed before anything serves":  "blog.example.com",
		"the connector hostname":                     "box.example.com",
		"the preview wildcard's probe":               edge.ProbeHostname(edge.PreviewWildcard("preview.example.com")),
	} {
		if !table.Admits(hostname) {
			t.Errorf("%s (%s) is refused a certificate: a name this box answers must be one the front proxy may order for", what, hostname)
		}
	}
	for what, hostname := range map[string]string{
		"a hostname nothing claims":              "unclaimed.example.com",
		"a preview hostname nothing claims":      "pr-7.preview.example.com",
		"the preview wildcard itself":            edge.PreviewWildcard("preview.example.com"),
		"the preview base":                       "preview.example.com",
		"a claimed hostname carrying a port":     "shop.example.com:443",
		"a claimed hostname with a trailing dot": "shop.example.com.",
		"nothing":                                "",
	} {
		if table.Admits(hostname) {
			t.Errorf("%s (%s) is admitted, and anyone who points a name at this box spends its CA allowance on it", what, hostname)
		}
	}
}

func TestTheTableRefusesEveryNameAPinnedCertificateCovers(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s",
		"claims":[
			{"owner":"ocel--shop--production","hostname":"shop.example.com","pointer":"@production"},
			{"owner":"ocel--shop--production","hostname":"api.shop.example.com","pointer":"@production"},
			{"owner":"ocel--shop--production","hostname":"deep.api.shop.example.com","pointer":"@production"},
			{"owner":"ocel--blog--production","hostname":"blog.example.com","pointer":"@production"}],
		"pins":[
			{"hostname":"Shop.Example.com","path":"/var/lib/ocel/certs/shop"},
			{"hostname":"*.shop.example.com","path":"/var/lib/ocel/certs/wild"}],
		"connector":"box.shop.example.com"}`)

	for what, hostname := range map[string]string{
		"a claimed hostname pinned by name":          "shop.example.com",
		"a claimed hostname under a pinned wildcard": "api.shop.example.com",
		"the connector under a pinned wildcard":      "box.shop.example.com",
		"a pinned hostname spelled in another case":  "SHOP.example.COM",
	} {
		if table.Admits(hostname) {
			t.Errorf("%s (%s) is admitted: the proxy serves a pinned name off its pin, and a certificate ordered for it stays the exact match caddy serves ahead of the pin until caddy restarts", what, hostname)
		}
	}
	for what, hostname := range map[string]string{
		"a claimed hostname no pin covers":                 "blog.example.com",
		"a claimed hostname two labels under the wildcard": "deep.api.shop.example.com",
	} {
		if !table.Admits(hostname) {
			t.Errorf("%s (%s) is refused, and no pin serves it", what, hostname)
		}
	}
}

func TestATableWithNoPreviewEntryAdmitsNoProbe(t *testing.T) {
	t.Parallel()

	table := mustRead(t, `{"grace":"30s"}`)
	if probe := edge.ProbeHostname(edge.PreviewWildcard("preview.example.com")); table.Admits(probe) {
		t.Errorf("a box with no preview entry admits %s", probe)
	}
}

func admitting(t *testing.T, board *switchboard.Board) (string, *http.Client) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "admit.sock")
	if len(socket) > 100 {
		t.Skipf("a unix socket path this host accepts does not fit under %s", socket)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = board.ServeAdmit(listener) }()
	t.Cleanup(func() { _ = board.Close() })
	return "http://switchboard", &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
}

func asked(t *testing.T, client *http.Client, method, url string) int {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	said, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	said.Body.Close()
	return said.StatusCode
}

func TestTheFrontProxyIsAnsweredWhetherAHostnameIsAdmittedAndNothingElseIs(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	board, _ := standing(t, routing(t, map[string]string{"shop.example.com": web}))
	at, front := admitting(t, board)

	if said := asked(t, front, http.MethodGet, at+switchboard.AdmitPath+"?domain=shop.example.com"); said != http.StatusOK {
		t.Errorf("the front proxy asking for a claimed hostname was answered %d, want 200", said)
	}
	for what, url := range map[string]string{
		"a hostname nothing claims":      at + switchboard.AdmitPath + "?domain=unclaimed.example.com",
		"no hostname at all":             at + switchboard.AdmitPath,
		"a claimed hostname at any path": at + "/?domain=shop.example.com",
		"a claimed hostname twice over":  at + switchboard.AdmitPath + "?domain=unclaimed.example.com&domain=shop.example.com",
	} {
		if said := asked(t, front, http.MethodGet, url); said/100 == 2 {
			t.Errorf("%s (%s) was answered %d, want a refusal", what, url, said)
		}
	}
	if said := asked(t, front, http.MethodPost, at+switchboard.AdmitPath+"?domain=shop.example.com"); said/100 == 2 {
		t.Errorf("a POST to the admit endpoint was answered %d, want it refused: it answers one question and changes nothing", said)
	}

	elsewhere := httptest.NewServer(board.Admit())
	t.Cleanup(elsewhere.Close)
	if said := asked(t, http.DefaultClient, http.MethodGet, elsewhere.URL+switchboard.AdmitPath+"?domain=shop.example.com"); said != http.StatusForbidden {
		t.Errorf("a peer that did not arrive over the admit socket was answered %d for a claimed hostname, want 403: only the front proxy holds that socket", said)
	}
}

func TestALoadedTableIsWhatTheNextAdmissionReads(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	board, _ := standing(t, routing(t, map[string]string{"shop.example.com": web}))
	at, front := admitting(t, board)
	path := filepath.Join(t.TempDir(), "routing.json")
	if err := os.WriteFile(path, routing(t, map[string]string{"blog.example.com": web}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := board.Load(path); err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if said := asked(t, front, http.MethodGet, at+switchboard.AdmitPath+"?domain=blog.example.com"); said != http.StatusOK {
		t.Errorf("a hostname the loaded table claims was answered %d, want 200", said)
	}
	if said := asked(t, front, http.MethodGet, at+switchboard.AdmitPath+"?domain=shop.example.com"); said == http.StatusOK {
		t.Error("a hostname unbound by the loaded table is still admitted, so its certificate renews for a name nothing claims")
	}
}

type held struct {
	net.Listener
	answering chan struct{}
	release   chan struct{}
}

func (l held) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &withheld{Conn: conn, answering: l.answering, release: l.release}, nil
}

type withheld struct {
	net.Conn
	answering chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (c *withheld) Write(answer []byte) (int, error) {
	c.once.Do(func() {
		close(c.answering)
		<-c.release
	})
	return c.Conn.Write(answer)
}

func TestShutdownFinishesAnAnswerTheAdmissionHadAlreadyBegun(t *testing.T) {
	t.Parallel()

	web := backend(t, "web")
	board, _ := standing(t, routing(t, map[string]string{"shop.example.com": web}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	answering, release := make(chan struct{}), make(chan struct{})
	go func() { _ = board.ServeAdmit(held{Listener: listener, answering: answering, release: release}) }()
	asking, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = asking.Close() })
	if _, err := io.WriteString(asking, "GET "+switchboard.AdmitPath+"?"+switchboard.AdmitField+"=shop.example.com HTTP/1.1\r\nHost: switchboard\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-answering

	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	shut := make(chan error, 1)
	go func() { shut <- board.Shutdown(ctx) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	said, err := http.ReadResponse(bufio.NewReader(asking), nil)
	if err != nil {
		t.Fatalf("the admission's answer under way was cut while shutting down: %v", err)
	}
	said.Body.Close()
	if said.StatusCode != http.StatusOK {
		t.Errorf("the admission's answer under way for a claimed hostname arrived as %d while shutting down, want 200: caddy denies an order it hears no answer to", said.StatusCode)
	}
	if err := <-shut; err != nil {
		t.Errorf("Shutdown() = %v after the answer under way was delivered", err)
	}
}
