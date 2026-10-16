package switchboard

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func (b *Board) DialConnectorAt(socket string) { b.connector = socket }

func (b *Board) Admit() http.Handler { return b.admit() }

func CheckGateOverHTTP(t *testing.T, held gate.Gate, req gate.Request, now time.Time) gate.Verdict {
	r, err := http.ReadRequest(bufio.NewReader(strings.NewReader(req.Method + " " + req.Target + " HTTP/1.1\r\nHost: " + req.Host + "\r\n\r\n")))
	if err != nil {
		t.Fatalf("the vector's request does not parse as HTTP: %v", err)
	}
	if r.URL.RequestURI() != req.Target {
		t.Skipf("Go's server hands the switchboard the target %q, not %q: the gate never sees the raw form", r.URL.RequestURI(), req.Target)
	}
	for name, values := range req.Header {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	answered := httptest.NewRecorder()
	keepPrivate, passed := answerGate(answered, r, &held, req.RevalidateTokenDigest, func(http.Header) {}, now)
	if !passed {
		return gate.Verdict{Response: &gate.Response{
			Status: answered.Code,
			Header: gate.Header(answered.Header()),
			Body:   answered.Body.String(),
		}}
	}
	response := http.Header{}
	keepPrivate(response)
	return gate.Verdict{Forward: gate.Header(r.Header), ResponseHeader: gate.Header(response)}
}
