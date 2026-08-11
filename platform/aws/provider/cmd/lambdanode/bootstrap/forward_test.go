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

type capturedResponse struct {
	body        []byte
	trailer     http.Header
	contentType string
	mode        string
}

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

func TestHandleInvocationForward(t *testing.T) {
	t.Run("streams prelude and body", func(t *testing.T) {
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
		m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

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
	})

	t.Run("node observes the public host", func(t *testing.T) {
		var observed string
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observed = r.Host
			io.WriteString(w, "ok")
		}))
		defer node.Close()

		event := `{"version":"2.0","rawPath":"/","requestContext":{"http":{"method":"GET"}},` +
			`"headers":{"host":"abc.lambda-url.us-east-1.on.aws","x-forwarded-host":"app.ocel.site"}}`
		rt, _ := fakeRuntime(t, []byte(event))
		m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

		if err := handleInvocation(t.Context(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}
		if observed != "app.ocel.site" {
			t.Errorf("node observed Host = %q, want app.ocel.site", observed)
		}
	})

	t.Run("app redirect is not followed", func(t *testing.T) {
		for _, status := range []int{
			http.StatusMovedPermanently,
			http.StatusFound,
			http.StatusSeeOther,
			http.StatusTemporaryRedirect,
			http.StatusPermanentRedirect,
		} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				var targetHits int
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					targetHits++
					io.WriteString(w, "the redirect target's own body")
				}))
				defer target.Close()

				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL, status)
				}))
				defer node.Close()

				rt, cap := fakeRuntime(t, []byte(getEvent))
				m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

				if err := handleInvocation(t.Context(), rt, m); err != nil {
					t.Fatalf("handleInvocation: %v", err)
				}

				p, body := splitPrelude(t, cap.body)
				if p.StatusCode != status {
					t.Errorf("statusCode = %d, want %d", p.StatusCode, status)
				}
				if p.Headers["Location"] != target.URL {
					t.Errorf("Location header = %q, want %q", p.Headers["Location"], target.URL)
				}
				if targetHits != 0 {
					t.Errorf("bootstrap fetched the redirect target %d times; it must forward the 3xx untouched", targetHits)
				}
				if strings.Contains(string(body), "the redirect target's own body") {
					t.Errorf("body = %q, want the app's redirect body, not the target's", body)
				}
			})
		}
	})

	t.Run("empty body travels as sentinel byte", func(t *testing.T) {
		for _, status := range []int{
			http.StatusOK,
			http.StatusTemporaryRedirect,
			http.StatusNotFound,
			http.StatusMethodNotAllowed,
			http.StatusInternalServerError,
		} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
				}))
				defer node.Close()

				rt, cap := fakeRuntime(t, []byte(getEvent))
				m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

				if err := handleInvocation(t.Context(), rt, m); err != nil {
					t.Fatalf("handleInvocation: %v", err)
				}

				p, body := splitPrelude(t, cap.body)
				if p.StatusCode != status {
					t.Errorf("statusCode = %d, want %d", p.StatusCode, status)
				}
				if p.Headers[emptyBodyHeader] != "1" {
					t.Errorf("%s header = %q, want 1", emptyBodyHeader, p.Headers[emptyBodyHeader])
				}
				if string(body) != emptyBodySentinel {
					t.Errorf("body = %q, want the sentinel byte %q", body, emptyBodySentinel)
				}
			})
		}
	})

	t.Run("self terminating statuses carry no sentinel", func(t *testing.T) {
		for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
				}))
				defer node.Close()

				rt, cap := fakeRuntime(t, []byte(getEvent))
				m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

				if err := handleInvocation(t.Context(), rt, m); err != nil {
					t.Fatalf("handleInvocation: %v", err)
				}

				p, body := splitPrelude(t, cap.body)
				if p.StatusCode != status {
					t.Errorf("statusCode = %d, want %d", p.StatusCode, status)
				}
				if _, marked := p.Headers[emptyBodyHeader]; marked {
					t.Errorf("%s set on a status that terminates on its own", emptyBodyHeader)
				}
				if len(body) != 0 {
					t.Errorf("body = %q, want no bytes at all on a %d", body, status)
				}
			})
		}
	})

	t.Run("bodied response is unmarked and intact", func(t *testing.T) {
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "x")
		}))
		defer node.Close()

		rt, cap := fakeRuntime(t, []byte(getEvent))
		m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

		if err := handleInvocation(t.Context(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}

		p, body := splitPrelude(t, cap.body)
		if _, marked := p.Headers[emptyBodyHeader]; marked {
			t.Errorf("bodied response carries %s; the edge would drop its body", emptyBodyHeader)
		}
		if string(body) != "x" {
			t.Errorf("body = %q, want x", body)
		}
	})

	t.Run("pre first byte failure is 502", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		deadPort := l.Addr().(*net.TCPAddr).Port
		l.Close()

		rt, cap := fakeRuntime(t, []byte(getEvent))
		m := &Membrane{nodePort: deadPort, client: newLoopbackClient()}

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
	})

	t.Run("mid stream failure sets error trailer", func(t *testing.T) {
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
		}))
		defer node.Close()

		rt, cap := fakeRuntime(t, []byte(getEvent))
		m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

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
	})
}
