package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// capturedResponse records what the fake Runtime API received on the streaming
// POST /response, so a test can assert the exact bytes the bootstrap emitted.
type capturedResponse struct {
	body        []byte
	trailer     http.Header
	contentType string
	mode        string
}

// fakeRuntime stands up an httptest Runtime API that serves one invocation
// (event) on /next and captures the streamed response.
func fakeRuntime(t *testing.T, event []byte) (*runtimeClient, *capturedResponse) {
	t.Helper()
	cap := &capturedResponse{}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+runtimeAPIVersion+"/runtime/invocation/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Lambda-Runtime-Aws-Request-Id", "req-1")
		w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", "arn:aws:lambda:us-east-1:123:function:fn")
		w.Write(event)
	})
	mux.HandleFunc("/"+runtimeAPIVersion+"/runtime/invocation/req-1/response", func(w http.ResponseWriter, r *http.Request) {
		cap.contentType = r.Header.Get("Content-Type")
		cap.mode = r.Header.Get(headerResponseMode)
		body, _ := io.ReadAll(r.Body)
		cap.body = body
		cap.trailer = r.Trailer.Clone()
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newRuntimeClient(strings.TrimPrefix(srv.URL, "http://")), cap
}

func portOf(t *testing.T, s *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(portStr)
	return p
}

// splitPrelude separates the http-integration-response prelude JSON from the
// streamed body at the 8 null-byte separator.
func splitPrelude(t *testing.T, raw []byte) (prelude, []byte) {
	t.Helper()
	sep := bytes.Index(raw, make([]byte, preludeSeparatorLen))
	if sep < 0 {
		t.Fatalf("no %d null-byte separator in response: %q", preludeSeparatorLen, raw)
	}
	var p prelude
	if err := json.Unmarshal(raw[:sep], &p); err != nil {
		t.Fatalf("prelude JSON invalid: %v (%q)", err, raw[:sep])
	}
	return p, raw[sep+preludeSeparatorLen:]
}

const getEvent = `{"version":"2.0","rawPath":"/","requestContext":{"http":{"method":"GET"}}}`

func TestHandleInvocation_StreamsPreludeAndBody(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		io.WriteString(w, "chunk1")
		fl.Flush()
		io.WriteString(w, "chunk2")
		fl.Flush()
	}))
	defer node.Close()

	rt, cap := fakeRuntime(t, []byte(getEvent))
	m := &Membrane{nodePort: portOf(t, node), client: &http.Client{}}

	if err := handleInvocation(t.Context(), rt, m); err != nil {
		t.Fatalf("handleInvocation: %v", err)
	}

	if cap.mode != responseModeStreaming {
		t.Errorf("response mode = %q, want %q", cap.mode, responseModeStreaming)
	}
	if cap.contentType != contentTypeHTTPIntegration {
		t.Errorf("content-type = %q, want %q", cap.contentType, contentTypeHTTPIntegration)
	}
	p, body := splitPrelude(t, cap.body)
	if p.StatusCode != http.StatusOK {
		t.Errorf("statusCode = %d, want 200", p.StatusCode)
	}
	if p.Headers["Content-Type"] != "text/event-stream" {
		t.Errorf("Content-Type header = %q, want text/event-stream", p.Headers["Content-Type"])
	}
	if string(body) != "chunk1chunk2" {
		t.Errorf("body = %q, want chunk1chunk2", body)
	}
	if got := cap.trailer.Get(headerErrorType); got != "" {
		t.Errorf("unexpected error trailer on success: %q", got)
	}
}

func TestHandleInvocation_PreFirstByteFailureIs502(t *testing.T) {
	// Reserve then release a port so nothing is listening → connection refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	rt, cap := fakeRuntime(t, []byte(getEvent))
	m := &Membrane{nodePort: deadPort, client: &http.Client{}}

	if err := handleInvocation(t.Context(), rt, m); err != nil {
		t.Fatalf("handleInvocation: %v", err)
	}

	p, body := splitPrelude(t, cap.body)
	if p.StatusCode != http.StatusBadGateway {
		t.Errorf("statusCode = %d, want 502", p.StatusCode)
	}
	if !strings.Contains(string(body), "upstream request failed") {
		t.Errorf("body = %q, want it to mention upstream failure", body)
	}
	if got := cap.trailer.Get(headerErrorType); got != "" {
		t.Errorf("pre-first-byte failure must use the prelude, not a trailer; got trailer %q", got)
	}
}

func TestHandleInvocation_MidStreamFailureSetsErrorTrailer(t *testing.T) {
	// Declare more bytes than we write, then return: the server closes the
	// connection early and the client's body read fails after the prelude and
	// first bytes have already streamed.
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
	}))
	defer node.Close()

	rt, cap := fakeRuntime(t, []byte(getEvent))
	m := &Membrane{nodePort: portOf(t, node), client: &http.Client{}}

	if err := handleInvocation(t.Context(), rt, m); err != nil {
		t.Fatalf("handleInvocation: %v", err)
	}

	p, body := splitPrelude(t, cap.body)
	if p.StatusCode != http.StatusOK {
		t.Errorf("statusCode = %d, want 200 (prelude already sent before failure)", p.StatusCode)
	}
	if !bytes.HasPrefix(body, []byte("partial")) {
		t.Errorf("body = %q, want it to start with the partial bytes", body)
	}
	if got := cap.trailer.Get(headerErrorType); got != errTypeUpstream {
		t.Errorf("error trailer = %q, want %q", got, errTypeUpstream)
	}
}
