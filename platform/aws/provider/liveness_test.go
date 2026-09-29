package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/router"
)

func TestAHostnameOnAnEmulatedAccountIsProbedWhereTheEmulatorAnswers(t *testing.T) {
	t.Parallel()

	var asked []string
	emulator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Host)
		w.Header().Set(router.HeaderRouter, "api-gateway")
	}))
	t.Cleanup(emulator.Close)

	p := NewProvider(Options{}, nil, aws.Config{BaseEndpoint: aws.String(emulator.URL)}, defaultNamespace)
	kind, err := p.ServingRouter(context.Background(), "web-j-1-node.journey.test")
	if err != nil || kind != "api-gateway" {
		t.Fatalf("Serving() = %q, %v (%s), want the router the emulator answered as: a journey.test name has no public DNS, and the emulator's endpoint is where its front answers", kind, err, p.LastProbeFailure("web-j-1-node.journey.test"))
	}
	if len(asked) != 1 || asked[0] != "web-j-1-node.journey.test" {
		t.Errorf("the emulator was asked for %v, want the hostname being probed", asked)
	}
}
