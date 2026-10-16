package edgeconformance

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

//go:embed gate.json
var gateVectors []byte

type gateVector struct {
	NowSeconds int64      `json:"nowSeconds"`
	Gate       gate.Gate  `json:"gate"`
	Cases      []gateCase `json:"cases"`
}

type gateCase struct {
	Name    string          `json:"name"`
	Gate    json.RawMessage `json:"gate"`
	Request struct {
		Method                string                  `json:"method"`
		Host                  string                  `json:"host"`
		Target                string                  `json:"target"`
		RevalidateTokenDigest string                  `json:"revalidateTokenDigest"`
		Headers               map[string]headerValues `json:"headers"`
	} `json:"request"`
	Expect struct {
		Forward              bool              `json:"forward"`
		ForwardHeaders       map[string]string `json:"forwardHeaders"`
		ForwardHeadersAbsent []string          `json:"forwardHeadersAbsent"`
		ResponseHeaders      map[string]string `json:"responseHeaders"`
		Status               int               `json:"status"`
		Headers              map[string]string `json:"headers"`
		HeadersAbsent        []string          `json:"headersAbsent"`
		BodyContains         []string          `json:"bodyContains"`
		BodyExcludes         []string          `json:"bodyExcludes"`
	} `json:"expect"`
}

type headerValues []string

func (v *headerValues) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*v = headerValues{single}
		return nil
	}
	return json.Unmarshal(data, (*[]string)(v))
}

type GateCheck func(t *testing.T, gate gate.Gate, req gate.Request, now time.Time) gate.Verdict

func RunGate(t *testing.T) {
	t.Helper()

	RunGateWith(t, func(_ *testing.T, held gate.Gate, req gate.Request, now time.Time) gate.Verdict {
		return held.Check(req, now)
	})
}

func RunGateWith(t *testing.T, check GateCheck) {
	t.Helper()

	var vectors gateVector
	if err := json.Unmarshal(gateVectors, &vectors); err != nil {
		t.Fatalf("the gate vectors do not parse: %v", err)
	}
	now := time.Unix(vectors.NowSeconds, 0)

	for _, example := range vectors.Cases {
		t.Run(example.Name, func(t *testing.T) {
			held := vectors.Gate
			held.BypassSecrets = slices.Clone(held.BypassSecrets)
			held.AllowOptions = slices.Clone(held.AllowOptions)
			if len(example.Gate) > 0 {
				if err := json.Unmarshal(example.Gate, &held); err != nil {
					t.Fatalf("the case's gate does not parse: %v", err)
				}
			}
			header := gate.Header{}
			for name, values := range example.Request.Headers {
				for _, value := range values {
					header.Add(name, value)
				}
			}
			verdict := check(t, held, gate.Request{
				Method:                example.Request.Method,
				Host:                  example.Request.Host,
				Target:                example.Request.Target,
				Header:                header,
				RevalidateTokenDigest: example.Request.RevalidateTokenDigest,
			}, now)

			if example.Expect.Forward {
				if verdict.Response != nil {
					t.Fatalf("Check answered %d, want the request forwarded", verdict.Response.Status)
				}
				for name, want := range example.Expect.ForwardHeaders {
					if got := verdict.Forward.Get(name); got != want {
						t.Errorf("forwarded %s = %q, want %q", name, got, want)
					}
				}
				for _, name := range example.Expect.ForwardHeadersAbsent {
					if verdict.Forward.Values(name) != nil {
						t.Errorf("forwarded %s = %q, want it removed", name, verdict.Forward.Values(name))
					}
				}
				for name, want := range example.Expect.ResponseHeaders {
					if got := verdict.ResponseHeader.Get(name); got != want {
						t.Errorf("response header %s = %q, want %q", name, got, want)
					}
				}
				return
			}

			if verdict.Response == nil {
				t.Fatalf("Check forwarded the request, want %d", example.Expect.Status)
			}
			if verdict.Response.Status != example.Expect.Status {
				t.Errorf("status = %d, want %d", verdict.Response.Status, example.Expect.Status)
			}
			for name, want := range example.Expect.Headers {
				if got := verdict.Response.Header.Get(name); got != want {
					t.Errorf("header %s = %q, want %q", name, got, want)
				}
			}
			for _, name := range example.Expect.HeadersAbsent {
				if verdict.Response.Header.Values(name) != nil {
					t.Errorf("header %s = %q, want it absent", name, verdict.Response.Header.Values(name))
				}
			}
			for _, want := range example.Expect.BodyContains {
				if !strings.Contains(verdict.Response.Body, want) {
					t.Errorf("body lacks %q:\n%s", want, verdict.Response.Body)
				}
			}
			for _, unwanted := range example.Expect.BodyExcludes {
				if strings.Contains(verdict.Response.Body, unwanted) {
					t.Errorf("body contains %q:\n%s", unwanted, verdict.Response.Body)
				}
			}
		})
	}
}
