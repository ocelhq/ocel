package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/relay"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestTheBastionRefusesToStartWithNoTargetToForwardTo(t *testing.T) {
	t.Parallel()
	for _, written := range []string{"", " , ,"} {
		if _, err := newHandler(env(map[string]string{relay.AllowedEnv: written})); err == nil || !strings.Contains(err.Error(), relay.AllowedEnv) {
			t.Errorf("newHandler(%q) = %v, want a refusal naming %s", written, err, relay.AllowedEnv)
		}
	}
}

func TestTheBastionForwardsToTheTargetsItIsToldAndNoOther(t *testing.T) {
	t.Parallel()
	handler, err := newHandler(env(map[string]string{relay.AllowedEnv: "10.240.0.5:5432, 10.240.0.9:6379"}))
	if err != nil {
		t.Fatalf("newHandler() = %v", err)
	}

	for target, want := range map[string]int{
		"10.240.0.5:5432":    http.StatusUpgradeRequired,
		"10.240.0.9:6379":    http.StatusUpgradeRequired,
		"10.240.0.5:6379":    http.StatusForbidden,
		"169.254.169.254:80": http.StatusForbidden,
	} {
		recorded := httptest.NewRecorder()
		handler.ServeHTTP(recorded, httptest.NewRequest(http.MethodGet, relay.Path+"?"+relay.TargetParam+"="+target, nil))
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
