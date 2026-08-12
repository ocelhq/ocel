package main

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
)

type scriptedRoundTripper struct {
	calls []func(req *http.Request) (*http.Response, error)
	n     int
}

func (s *scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.n >= len(s.calls) {
		panic("scriptedRoundTripper: no more scripted calls")
	}
	fn := s.calls[s.n]
	s.n++
	return fn(req)
}

func reusedConnFailure(err error, wroteRequest bool) func(req *http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(req.Context())
		if trace != nil && trace.GotConn != nil {
			trace.GotConn(httptrace.GotConnInfo{Reused: true})
		}
		if wroteRequest && trace != nil && trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return nil, err
	}
}

func okResponse() func(req *http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}
}

const postEvent = `{"version":"2.0","rawPath":"/api/revalidate-layout","requestContext":{"http":{"method":"POST"}},"body":"payload"}`

func TestForwardToNodeRetriesStaleReusedConnection(t *testing.T) {
	ev, err := parseEvent([]byte(postEvent))
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}

	t.Run("EOF on a reused connection with nothing written succeeds on the single retry", func(t *testing.T) {
		rt := &scriptedRoundTripper{calls: []func(req *http.Request) (*http.Response, error){
			reusedConnFailure(io.EOF, false),
			okResponse(),
		}}
		m := &Membrane{nodePort: 1, client: &http.Client{Transport: rt}}

		resp, err := m.forwardToNode(t.Context(), ev)
		if err != nil {
			t.Fatalf("forwardToNode: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("statusCode = %d, want 200", resp.StatusCode)
		}
		if rt.n != 2 {
			t.Errorf("round trips = %d, want 2 (one failure, one retry)", rt.n)
		}
	})

	t.Run("failure after bytes reached the origin is not retried", func(t *testing.T) {
		rt := &scriptedRoundTripper{calls: []func(req *http.Request) (*http.Response, error){
			reusedConnFailure(io.EOF, true),
		}}
		m := &Membrane{nodePort: 1, client: &http.Client{Transport: rt}}

		_, err := m.forwardToNode(t.Context(), ev)
		if err == nil {
			t.Fatal("forwardToNode: want an error, got nil")
		}
		if rt.n != 1 {
			t.Errorf("round trips = %d, want 1 (a written POST must never be duplicated)", rt.n)
		}
	})

	t.Run("the retry itself is bounded to one attempt", func(t *testing.T) {
		rt := &scriptedRoundTripper{calls: []func(req *http.Request) (*http.Response, error){
			reusedConnFailure(io.EOF, false),
			reusedConnFailure(io.EOF, false),
		}}
		m := &Membrane{nodePort: 1, client: &http.Client{Transport: rt}}

		_, err := m.forwardToNode(t.Context(), ev)
		if err == nil {
			t.Fatal("forwardToNode: want an error, got nil")
		}
		if rt.n != 2 {
			t.Errorf("round trips = %d, want 2 (initial attempt plus exactly one retry)", rt.n)
		}
	})

	t.Run("a fresh connection's failure is not retried", func(t *testing.T) {
		rt := &scriptedRoundTripper{calls: []func(req *http.Request) (*http.Response, error){
			func(req *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(req.Context())
				if trace != nil && trace.GotConn != nil {
					trace.GotConn(httptrace.GotConnInfo{Reused: false})
				}
				return nil, io.EOF
			},
		}}
		m := &Membrane{nodePort: 1, client: &http.Client{Transport: rt}}

		_, err := m.forwardToNode(t.Context(), ev)
		if err == nil {
			t.Fatal("forwardToNode: want an error, got nil")
		}
		if rt.n != 1 {
			t.Errorf("round trips = %d, want 1 (a fresh connection's failure is a real failure)", rt.n)
		}
	})
}
