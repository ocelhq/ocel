package pricing_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

func TestTheURLIsTheFlagThenTheEnvironmentThenTheHostedPricer(t *testing.T) {
	t.Setenv(pricing.URLEnvVar, "")
	t.Setenv("OCEL_DEV", "")
	if got := pricing.ResolveBaseURL(""); got != pricing.DefaultBaseURL {
		t.Errorf("ResolveBaseURL() = %q, want %q", got, pricing.DefaultBaseURL)
	}
	t.Setenv("OCEL_DEV", "1")
	if got := pricing.ResolveBaseURL(""); got != "http://localhost:8090" {
		t.Errorf("ResolveBaseURL() under OCEL_DEV = %q, want the local pricer", got)
	}
	t.Setenv(pricing.URLEnvVar, "https://pricer.internal")
	if got := pricing.ResolveBaseURL(""); got != "https://pricer.internal" {
		t.Errorf("ResolveBaseURL() = %q, want the environment's pricer", got)
	}
	if got := pricing.ResolveBaseURL("https://typed.example"); got != "https://typed.example" {
		t.Errorf("ResolveBaseURL(flag) = %q, want what was typed", got)
	}
}

func TestAPricerThatDoesNotAnswerSaysWhereToGetOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := pricing.New(server.URL, "").Price(context.Background(), &costv1.PriceRequest{})

	if err == nil {
		t.Fatal("Price() = nil, want the refusal")
	}
	for _, want := range []string{server.URL, "docker compose up pricing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Price() = %v, want a sentence naming %q", err, want)
		}
	}
}
