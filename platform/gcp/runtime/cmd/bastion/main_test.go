package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func echoing(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func TestAStoppingBastionWaitsForTheConnectionsItIsRelayingWithinItsGrace(t *testing.T) {
	t.Parallel()
	target := echoing(t)
	handler, err := newHandler(env(map[string]string{relay.AllowedEnv: strings.Replace(target, ":", "/32:", 1)}))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan int, 1)
	go func() { served <- serve(ctx, listener, handler, time.Minute) }()
	forward, err := relay.OpenForward(context.Background(), relay.Link{URL: "http://" + listener.Addr().String(), Target: target,
		Token: func(context.Context) (string, error) { return "id-token", nil }})
	if err != nil {
		t.Fatalf("OpenForward() = %v", err)
	}
	conn, err := net.Dial("tcp", forward.Address())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}

	stop()
	select {
	case <-served:
		t.Fatal("the bastion stopped while a connection was still relayed through it")
	case <-time.After(200 * time.Millisecond):
	}
	_ = conn.Close()
	forward.Close()

	select {
	case code := <-served:
		if code != 0 {
			t.Errorf("serve() = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bastion never stopped once its last relayed connection ended")
	}
}

func TestAStoppingBastionStopsOnceItsGraceRunsOutWhateverItStillRelays(t *testing.T) {
	t.Parallel()
	target := echoing(t)
	handler, err := newHandler(env(map[string]string{relay.AllowedEnv: strings.Replace(target, ":", "/32:", 1)}))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan int, 1)
	go func() { served <- serve(ctx, listener, handler, 100*time.Millisecond) }()
	forward, err := relay.OpenForward(context.Background(), relay.Link{URL: "http://" + listener.Addr().String(), Target: target,
		Token: func(context.Context) (string, error) { return "id-token", nil }})
	if err != nil {
		t.Fatalf("OpenForward() = %v", err)
	}
	t.Cleanup(forward.Close)
	conn, err := net.Dial("tcp", forward.Address())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}

	stop()

	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the bastion outlived its grace waiting on a connection")
	}
}

func TestTheBastionRefusesToStartWithNoDestinationOrOneItCannotRead(t *testing.T) {
	t.Parallel()
	for _, written := range []string{"", " , ,", "10.240.0.0/20", "db.internal:5432", "10.240.0.0/20:postgres", "10.240.0.0/20:70000"} {
		if _, err := newHandler(env(map[string]string{relay.AllowedEnv: written})); err == nil || !strings.Contains(err.Error(), relay.AllowedEnv) {
			t.Errorf("newHandler(%q) = %v, want a refusal naming %s", written, err, relay.AllowedEnv)
		}
	}
}

func TestTheBastionForwardsToTheRangesAndPortsItIsToldAndNoOther(t *testing.T) {
	t.Parallel()
	handler, err := newHandler(env(map[string]string{relay.AllowedEnv: "10.240.0.0/20:5432, 10.240.0.0/20:6379"}))
	if err != nil {
		t.Fatalf("newHandler() = %v", err)
	}

	for target, want := range map[string]int{
		"10.240.0.5:5432":             http.StatusUpgradeRequired,
		"10.240.15.254:6379":          http.StatusUpgradeRequired,
		"10.240.0.5:22":               http.StatusForbidden,
		"10.240.16.1:5432":            http.StatusForbidden,
		"169.254.169.254:80":          http.StatusForbidden,
		"169.254.169.254:5432":        http.StatusForbidden,
		"8.8.8.8:5432":                http.StatusForbidden,
		"metadata.google.internal:80": http.StatusForbidden,
		"localhost:5432":              http.StatusForbidden,
		"[::ffff:10.240.0.5]:5432":    http.StatusUpgradeRequired,
	} {
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, httptest.NewRequest(http.MethodGet, relay.Path+"?"+url.Values{relay.TargetParam: {target}}.Encode(), nil))
		if recorded.Code != want {
			t.Errorf("a request for %s was answered %d, want %d", target, recorded.Code, want)
		}
	}
}

func TestTheBastionListensOnThePortCloudRunNames(t *testing.T) {
	t.Parallel()
	if got := listenAddress(env(map[string]string{"PORT": "9090"})); got != ":9090" {
		t.Errorf("listenAddress() = %q, want :9090", got)
	}
	if got := listenAddress(env(nil)); got != ":8080" {
		t.Errorf("listenAddress() with no PORT = %q, want :8080, the port the service declares", got)
	}
}
