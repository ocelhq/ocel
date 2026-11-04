package gcloud_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestReadingWhoATokenNamesIsASpanWithGooglesAnswer(t *testing.T) {
	ctx, spans := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		googletest.WriteBody(w, map[string]string{"email": "deploy@acme.iam.gserviceaccount.com"})
	}))
	t.Cleanup(server.Close)

	if _, err := gcloud.PrincipalNamed(ctx, server.URL, "ya29.stub"); err != nil {
		t.Fatal(err)
	}

	asked := spans.Only(t)
	if asked.Name() != "GET "+server.Listener.Addr().String() {
		t.Fatalf("reading the principal made span %q, want the GET it sent", asked.Name())
	}
	if answered := tracingtest.Attributes(asked)["http.response.status_code"]; answered.AsInt64() != http.StatusOK {
		t.Errorf("the span recorded status %d, want %d", answered.AsInt64(), http.StatusOK)
	}
}

func TestARefusedTokenLeavesGooglesAnswerOutOfTheTrace(t *testing.T) {
	const answer = `{"error":"invalid_token","error_description":"ya29.refused belongs to deploy@acme.iam.gserviceaccount.com"}`
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(answer))
	})
	ctx, spans := tracingtest.Record(t)

	if _, err := gcloud.PrincipalNamed(ctx, gcloud.TokenInfoURL, "ya29.refused"); err == nil {
		t.Fatal("PrincipalNamed() = nil, want the refusal")
	}

	asked := spans.Only(t)
	if exported := tracingtest.ExportedText(asked); strings.Contains(exported, "deploy@acme") {
		t.Errorf("trace carries Google's answer about the token:\n%s", exported)
	}
}
