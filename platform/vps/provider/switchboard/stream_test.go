package switchboard_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const upgradeProtocol = "echo"

func echoing(t *testing.T, name string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != upgradeProtocol {
			_, _ = io.WriteString(w, name)
			return
		}
		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+upgradeProtocol+"\r\n\r\n")
		for {
			said, err := buffered.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := io.WriteString(conn, name+" "+said); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

type socket struct {
	conn   net.Conn
	reader *bufio.Reader
}

func upgrading(t *testing.T, at, host string) socket {
	t.Helper()
	conn, err := net.Dial("tcp", at)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", host, upgradeProtocol)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("the upgrade through the switchboard answered nothing: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("the upgrade answered %d, want 101", response.StatusCode)
	}
	if response.Header.Get(edge.HeaderEdge) != switchboard.EdgeName {
		t.Errorf("the upgrade answered naming the edge %q, want %q", response.Header.Get(edge.HeaderEdge), switchboard.EdgeName)
	}
	return socket{conn: conn, reader: reader}
}

func (s socket) exchange(word string) (string, error) {
	if _, err := io.WriteString(s.conn, word+"\n"); err != nil {
		return "", err
	}
	said, err := s.reader.ReadString('\n')
	return strings.TrimSuffix(said, "\n"), err
}

func TestAnUpgradedConnectionIsPassedThroughBothWays(t *testing.T) {
	t.Parallel()

	web := echoing(t, "web")
	_, at := standing(t, routing(t, map[string]string{"shop.example.com": web}))

	held := upgrading(t, at, "shop.example.com")
	for _, word := range []string{"one", "two", "three"} {
		if said, err := held.exchange(word); err != nil || said != "web "+word {
			t.Fatalf("the upgraded connection answered %q, %v to %q, want the upstream echoing it", said, err, word)
		}
	}
}

func TestAStreamedResponseReachesTheClientAsEachEventIsFlushedRatherThanWhenItEnds(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		_ = http.NewResponseController(w).Flush()
		<-release
		_, _ = io.WriteString(w, "data: last\n\n")
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	_, at := standing(t, routing(t, map[string]string{"shop.example.com": strings.TrimPrefix(server.URL, "http://")}))

	request, err := http.NewRequest(http.MethodGet, "http://"+at+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "shop.example.com"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make(chan string, 1)
	go func() {
		said, _ := bufio.NewReader(response.Body).ReadString('\n')
		first <- said
	}()
	select {
	case said := <-first:
		if said != "data: first\n" {
			t.Errorf("the stream's first line read %q, want the first event", said)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first event never reached the client while the upstream held the stream open: a buffered event stream is a dead one")
	}
}

func TestTheConnectorPathReachesTheConnectorOverItsSocketWithThePrefixStripped(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "connector.sock")
	if len(path) > 100 {
		t.Skipf("a unix socket path this host accepts does not fit under %s", path)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	connector := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-Path", r.URL.Path)
		w.Header().Set("X-Served-Host", r.Host)
		_, _ = io.WriteString(w, "connector")
	}))
	connector.Listener = listener
	connector.Start()
	t.Cleanup(connector.Close)

	web := backend(t, "web")
	table, err := switchboard.Read([]byte(`{"grace":"30s",
		"claims":[{"owner":"ocel--shop--production","hostname":"box.example.com","pointer":"@production"}],
		"routes":[{"owner":"ocel--shop--production","pointer":"@production","app":"web","upstream":"` + web + `"}],
		"connector":"box.example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	board := switchboard.New(table, nil)
	board.DialConnectorAt(path)
	served, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = board.Serve(served) }()
	t.Cleanup(func() { _ = board.Close() })
	at := served.Addr().String()

	said := ask(t, http.DefaultClient, at, "box.example.com", switchboard.ConnectorPath+"/connector.v1.Box/Describe")
	if said.body != "connector" || said.header.Get("X-Served-Path") != "/connector.v1.Box/Describe" {
		t.Errorf("the connector path answered %q asking for %q, want the connector asked for the procedure without the prefix", said.body, said.header.Get("X-Served-Path"))
	}
	if said.header.Get("X-Served-Host") != "box.example.com" || said.header.Get(edge.HeaderEdge) != switchboard.EdgeName {
		t.Errorf("the connector was asked for host %q and answered naming %q, want box.example.com and %s", said.header.Get("X-Served-Host"), said.header.Get(edge.HeaderEdge), switchboard.EdgeName)
	}
	if said := ask(t, http.DefaultClient, at, "box.example.com", "/"); said.body != "web" {
		t.Errorf("the connector's hostname answered %q off the connector path, want the app that claims it", said.body)
	}
}
