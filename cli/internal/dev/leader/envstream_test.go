package leader

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestARefusedEnvSubscriptionIsTracedWithoutTheEnvTheLeaderSent(t *testing.T) {
	ctx, calls := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`data: {"DATABASE_URL":"value-the-user-set"}` + "\n\n"))
	}))
	t.Cleanup(server.Close)

	if _, err := Subscribe(ctx, Leader{Address: strings.TrimPrefix(server.URL, "http://"), Token: "leader-token"}); err == nil {
		t.Fatal("Subscribe = nil, want the leader's refusal")
	}

	span := calls.Only(t)
	if status := tracingtest.Attributes(span)["http.response.status_code"]; status.AsInt64() != http.StatusServiceUnavailable {
		t.Errorf("http.response.status_code = %v, want the refusal traced", status)
	}
	if body, recorded := tracingtest.Attributes(span)["http.response.body"]; recorded {
		t.Errorf("http.response.body = %q, want no body on the stream that carries the user's env", body.AsString())
	}
}
