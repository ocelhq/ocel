package main

import (
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func answeringAt(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) string {
	t.Helper()
	socket := frontAt(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(answer)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

func TestHoldsPassesASocketThatAnswersWhatItWasAskedFor(t *testing.T) {
	asked := make(chan string, 1)
	socket := answeringAt(t, func(w http.ResponseWriter, r *http.Request) {
		asked <- r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"servers":{"ocel":{"listen":[":443"]}}}`)
	})

	if code, _, errs := ran(t, "holds", socket, "/config/apps/http"); code != 0 {
		t.Fatalf("holds over a socket answering the http app = %d, %q, want 0", code, errs)
	}
	select {
	case got := <-asked:
		if got != "GET /config/apps/http" {
			t.Errorf("holds asked %q, want a read of the path it was handed and nothing that could change what the socket serves", got)
		}
	default:
		t.Error("holds passed without asking the socket anything")
	}
}

func TestHoldsRefusesASocketThatIsThereAndHoldsNothingOrAnswersNothing(t *testing.T) {
	stale := frontAt(t)
	left, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	left.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = left.Close()

	for what, held := range map[string]struct {
		socket string
		says   string
	}{
		"a socket file a crashed process left behind": {stale, "connection refused"},
		"no socket at all": {filepath.Join(t.TempDir(), "absent.sock"), "no such file"},
		"an endpoint whose app is not loaded": {answeringAt(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "null\n")
		}), "holds nothing at /config/apps/http"},
		"an endpoint that refuses the read": {answeringAt(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unknown path", http.StatusBadRequest)
		}), "400"},
	} {
		code, _, errs := ran(t, "holds", held.socket, "/config/apps/http")
		if code != exitNotServingYet || !strings.Contains(errs, held.says) {
			t.Errorf("holds over %s = %d, %q, want %d saying %q: a front proxy is ready only once its admin api answers and its http app is loaded", what, code, errs, exitNotServingYet, held.says)
		}
	}
	if code, _, _ := ran(t, "holds", stale); code != exitRefused {
		t.Errorf("holds with no path = %d, want the usage refusal", code)
	}
}
