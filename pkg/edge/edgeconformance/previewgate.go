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
	Now   int64             `json:"now"`
	Gate  edge.PreviewGate  `json:"gate"`
	Cases []previewGateCase `json:"cases"`
}

type previewGateCase struct {
	Name    string          `json:"name"`
	Gate    json.RawMessage `json:"gate"`
	Request struct {
		Method  string            `json:"method"`
		Host    string            `json:"host"`
		Target  string            `json:"target"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Expect struct {
		Forward         bool              `json:"forward"`
		ForwardHeaders  map[string]string `json:"forwardHeaders"`
		ResponseHeaders map[string]string `json:"responseHeaders"`
		Status          int               `json:"status"`
		Headers         map[string]string `json:"headers"`
		BodyContains    []string          `json:"bodyContains"`
		BodyExcludes    []string          `json:"bodyExcludes"`
		Absent          []string          `json:"absent"`
	} `json:"expect"`
}

func RunPreviewGate(t *testing.T) {
	t.Helper()

	var vectors previewGateVector
	if err := json.Unmarshal(previewGateVectors, &vectors); err != nil {
		t.Fatalf("the preview gate vectors do not parse: %v", err)
	}
	now := time.Unix(vectors.Now, 0)

	for _, c := range vectors.Cases {
		t.Run(c.Name, func(t *testing.T) {
			gate := vectors.Gate
			gate.BypassSecrets = slices.Clone(gate.BypassSecrets)
			gate.AllowOptions = slices.Clone(gate.AllowOptions)
			if len(c.Gate) > 0 {
				if err := json.Unmarshal(c.Gate, &gate); err != nil {
					t.Fatalf("the case's gate does not parse: %v", err)
				}
			}
			header := http.Header{}
			for name, value := range c.Request.Headers {
				header.Set(name, value)
			}
			verdict := gate.Check(edge.PreviewRequest{
				Method: c.Request.Method,
				Host:   c.Request.Host,
				Target: c.Request.Target,
				Header: header,
				Body:   []byte(c.Request.Body),
			}, now)

			if c.Expect.Forward {
				if verdict.Response != nil {
					t.Fatalf("Check answered %d, want the request forwarded", verdict.Response.Status)
				}
				for name, want := range c.Expect.ForwardHeaders {
					if got := verdict.Forward.Get(name); got != want {
						t.Errorf("forwarded %s = %q, want %q", name, got, want)
					}
				}
				for _, name := range c.Expect.Absent {
					if verdict.Forward.Values(name) != nil {
						t.Errorf("forwarded %s = %q, want it removed", name, verdict.Forward.Values(name))
					}
				}
				for name, want := range c.Expect.ResponseHeaders {
					if got := verdict.ResponseHeader.Get(name); got != want {
						t.Errorf("response header %s = %q, want %q", name, got, want)
					}
				}
				return
			}

			if verdict.Response == nil {
				t.Fatalf("Check forwarded the request, want %d", c.Expect.Status)
			}
			if verdict.Response.Status != c.Expect.Status {
				t.Errorf("status = %d, want %d", verdict.Response.Status, c.Expect.Status)
			}
			for name, want := range c.Expect.Headers {
				if got := verdict.Response.Header.Get(name); got != want {
					t.Errorf("header %s = %q, want %q", name, got, want)
				}
			}
			for _, name := range c.Expect.Absent {
				if verdict.Response.Header.Values(name) != nil {
					t.Errorf("header %s = %q, want it absent", name, verdict.Response.Header.Values(name))
				}
			}
			for _, want := range c.Expect.BodyContains {
				if !strings.Contains(verdict.Response.Body, want) {
					t.Errorf("body lacks %q:\n%s", want, verdict.Response.Body)
				}
			}
			for _, unwanted := range c.Expect.BodyExcludes {
				if strings.Contains(verdict.Response.Body, unwanted) {
					t.Errorf("body contains %q:\n%s", unwanted, verdict.Response.Body)
				}
			}
		})
	}
}
