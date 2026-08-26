package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

type traceSpan struct {
	Name              string      `json:"name"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []traceAttr `json:"attributes"`
}

type traceAttr struct {
	Key   string `json:"key"`
	Value struct {
		IntValue *string `json:"intValue"`
	} `json:"value"`
}

func (s traceSpan) retryCount(t *testing.T) int {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key == "ocel.retry_count" && a.Value.IntValue != nil {
			n, err := strconv.Atoi(*a.Value.IntValue)
			if err != nil {
				t.Fatalf("parse retry_count %q: %v", *a.Value.IntValue, err)
			}
			return n
		}
	}
	t.Fatalf("span %q carries no ocel.retry_count attribute", s.Name)
	return -1
}

func (s traceSpan) durationNS(t *testing.T) int64 {
	t.Helper()
	return parseNano(t, s.EndTimeUnixNano) - parseNano(t, s.StartTimeUnixNano)
}

func parseNano(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q is not a plain integer: %v", s, err)
	}
	return n
}

func readTraceSpans(t *testing.T, root string) []traceSpan {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".ocel", "runs", "*.otlp.json"))
	if err != nil {
		t.Fatalf("glob trace files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("trace files under %s = %v, want exactly one", root, matches)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []traceSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("trace file is not valid OTLP/JSON: %v", err)
	}
	var spans []traceSpan
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	return spans
}

func spansNamed(spans []traceSpan, name string) []traceSpan {
	var out []traceSpan
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func rootSpan(t *testing.T, spans []traceSpan) traceSpan {
	t.Helper()
	for _, s := range spans {
		if s.ParentSpanID == "" {
			return s
		}
	}
	t.Fatal("no root span (a span with no parent) in the trace file")
	return traceSpan{}
}

func TestGateRecoveryTracesEachAttemptAndTheHumanWait(t *testing.T) {
	root := clitest.SetUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	problems := problemsFile(t, missingStripeKey)
	deps := clitest.NewDeps()
	terminalStdin(&deps)
	var mu sync.Mutex
	var opened []string
	recordBrowser(&deps, &opened, &mu)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	address, token := awaitVarsUI(t, &out, 1)
	setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
	clitest.WriteFile(t, problems, "[]")
	markDone(t, address, token)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, out.String(), stderr.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("runDeploy never returned after the matrix was marked done")
	}

	spans := readTraceSpans(t, root)

	builds := spansNamed(spans, "build")
	if len(builds) != 2 {
		t.Fatalf("got %d spans named %q, want 2 (the refused attempt and the resumed one)", len(builds), "build")
	}
	seen := map[int]bool{}
	for _, b := range builds {
		seen[b.retryCount(t)] = true
	}
	if !seen[0] || !seen[1] {
		t.Errorf("build span retry_count values = %v, want 0 and 1", seen)
	}

	waits := spansNamed(spans, "await_human_input")
	if len(waits) != 1 {
		t.Fatalf("got %d spans named %q, want exactly 1", len(waits), "await_human_input")
	}

	root0 := rootSpan(t, spans)
	for _, s := range append(append([]traceSpan{}, builds...), waits[0]) {
		if s.ParentSpanID != root0.SpanID {
			t.Errorf("span %q parent = %q, want the root span %q — a sibling of the build attempts, not nested in one", s.Name, s.ParentSpanID, root0.SpanID)
		}
	}

	var buildSum int64
	for _, b := range builds {
		buildSum += b.durationNS(t)
	}
	waitDuration := waits[0].durationNS(t)
	rootDuration := root0.durationNS(t)
	if rootDuration < buildSum+waitDuration {
		t.Errorf("root span duration = %dns, want it to cover both build attempts (%dns) and the wait (%dns)", rootDuration, buildSum, waitDuration)
	}
}
