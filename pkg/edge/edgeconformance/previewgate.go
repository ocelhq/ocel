package edgeconformance

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
)

//go:embed previewgate.json
var previewGateVectors []byte

type previewGateVector struct {
	NowSeconds int64             `json:"nowSeconds"`
	Gate       edge.PreviewGate  `json:"gate"`
	Cases      []previewGateCase `json:"cases"`
}

type previewGateCase struct {
	Name    string          `json:"name"`
	Gate    json.RawMessage `json:"gate"`
	Request struct {
		Method  string                  `json:"method"`
		Host    string                  `json:"host"`
		Target  string                  `json:"target"`
		Headers map[string]headerValues `json:"headers"`
		Body    string                  `json:"body"`
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

func RunPreviewGate(t *testing.T) {
	t.Helper()

	var vectors previewGateVector
	if err := json.Unmarshal(previewGateVectors, &vectors); err != nil {
		t.Fatalf("the preview gate vectors do not parse: %v", err)
	}
	now := time.Unix(vectors.NowSeconds, 0)

	for _, example := range vectors.Cases {
		t.Run(example.Name, func(t *testing.T) {
			gate := vectors.Gate
			gate.BypassSecrets = slices.Clone(gate.BypassSecrets)
			gate.AllowOptions = slices.Clone(gate.AllowOptions)
			if len(example.Gate) > 0 {
				if err := json.Unmarshal(example.Gate, &gate); err != nil {
					t.Fatalf("the case's gate does not parse: %v", err)
				}
			}
			header := http.Header{}
			for name, values := range example.Request.Headers {
				for _, value := range values {
					header.Add(name, value)
				}
			}
			verdict := gate.Check(edge.PreviewRequest{
				Method: example.Request.Method,
				Host:   example.Request.Host,
				Target: example.Request.Target,
				Header: header,
				Body:   []byte(example.Request.Body),
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
