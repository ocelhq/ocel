package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestTheBastionRefusesToStartWithNoDestinationOrOneItCannotRead(t *testing.T) {
	t.Parallel()
	for _, written := range []string{"", " , ,", "10.240.0.0/20", "db.internal:5432", "10.240.0.0/20:postgres", "10.240.0.0/20:70000"} {
		if _, err := newHandler(env(map[string]string{relay.AllowedEnv: written})); err == nil || !strings.Contains(err.Error(), relay.AllowedEnv) {
			t.Errorf("newHandler(%q) = %v, want a refusal naming %s", written, err, relay.AllowedEnv)
		}
	}
}

func TestTheBastionForwardsToTheRangesAndPortsItIsToldAndNoOther(t *testing.T) {
	t.Parallel()
	handler, err := newHandler(env(map[string]string{relay.AllowedEnv: "10.240.0.0/20:5432, 10.240.0.0/20:6379"}))
	if err != nil {
		t.Fatalf("newHandler() = %v", err)
	}

	for target, want := range map[string]int{
		"10.240.0.5:5432":             http.StatusUpgradeRequired,
		"10.240.15.254:6379":          http.StatusUpgradeRequired,
		"10.240.0.5:22":               http.StatusForbidden,
		"10.240.16.1:5432":            http.StatusForbidden,
		"169.254.169.254:80":          http.StatusForbidden,
		"169.254.169.254:5432":        http.StatusForbidden,
		"8.8.8.8:5432":                http.StatusForbidden,
		"metadata.google.internal:80": http.StatusForbidden,
		"localhost:5432":              http.StatusForbidden,
		"[::ffff:10.240.0.5]:5432":    http.StatusUpgradeRequired,
	} {
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, httptest.NewRequest(http.MethodGet, relay.Path+"?"+url.Values{relay.TargetParam: {target}}.Encode(), nil))
		if recorded.Code != want {
			t.Errorf("a request for %s was answered %d, want %d", target, recorded.Code, want)
		}
	}
}

func TestTheBastionListensOnThePortCloudRunNames(t *testing.T) {
	t.Parallel()
	if got := listenAddress(env(map[string]string{"PORT": "9090"})); got != ":9090" {
		t.Errorf("listenAddress() = %q, want :9090", got)
	}
	if got := listenAddress(env(nil)); got != ":8080" {
		t.Errorf("listenAddress() with no PORT = %q, want :8080, the port the service declares", got)
	}
}
