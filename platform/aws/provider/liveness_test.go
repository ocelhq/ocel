package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestAHostnameOnAnEmulatedAccountIsProbedWhereTheEmulatorAnswers(t *testing.T) {
	t.Parallel()

	var asked []string
	emulator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Host)
		w.Header().Set(edge.HeaderEdge, "api-gateway")
	}))
	t.Cleanup(emulator.Close)

	p := NewProvider(Options{}, nil, aws.Config{BaseEndpoint: aws.String(emulator.URL)}, defaultNamespace)
	kind, err := p.Serving(context.Background(), "api-gateway", "web-j-1-node.journey.test")
	if err != nil || kind != "api-gateway" {
		t.Fatalf("Serving() = %q, %v (%s), want the edge the emulator answered as: a journey.test name has no public DNS, and the emulator's endpoint is where its front answers", kind, err, p.Unreached("web-j-1-node.journey.test"))
	}
	if len(asked) != 1 || asked[0] != "web-j-1-node.journey.test" {
		t.Errorf("the emulator was asked for %v, want the hostname being probed", asked)
	}
}
