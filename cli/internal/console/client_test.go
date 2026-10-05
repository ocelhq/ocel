package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComposeUserAgentAddsTheAgentOnlyWhenOneIsDetected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, "ocel-cli"},
		{"an agent marker", map[string]string{"CLAUDECODE": "1"}, "ocel-cli agent/claude-code"},
		{"AI_AGENT", map[string]string{"AI_AGENT": "claude-code_2-1-289_agent"}, "ocel-cli agent/claude-code"},
		{"CI alone", map[string]string{"CI": "true"}, "ocel-cli"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(name string) string { return tt.env[name] }
			if got := composeUserAgent(getenv); got != tt.want {
				t.Errorf("composeUserAgent = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewSendsTheUserAgentComposedFromTheProcessEnvironment(t *testing.T) {
	t.Setenv("AI_AGENT", "ocel-test_1")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := New(srv.URL).send(context.Background(), http.MethodGet, "/", "", nil, nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if want := "ocel-cli agent/ocel-test"; got != want {
		t.Errorf("User-Agent = %q, want %q", got, want)
	}
}
