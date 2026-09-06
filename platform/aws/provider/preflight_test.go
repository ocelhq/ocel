package provider

import (
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAContainerAppIsRefusedBehindEveryEdgeButTheDefault(t *testing.T) {
	t.Parallel()

	apps := []providerkit.AppEntry{
		{App: "web", Manifest: &contractv1.ManifestApp{Name: "web", Compute: string(providerkit.ComputeContainer)}},
		{App: "api", Manifest: &contractv1.ManifestApp{Name: "api", Compute: string(providerkit.ComputeServerless)}},
	}
	err := refuseContainersBehindFunctionEdge(providerkit.DeployPreflight{Edge: apigateway.Kind, Plan: providerkit.DeployPlan{Apps: apps}})
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), string(apigateway.Kind)) {
		t.Fatalf("preflight behind %s = %v, want the container app refused by name: that edge invokes a function and would fail at promote otherwise", apigateway.Kind, err)
	}
	if err := refuseContainersBehindFunctionEdge(providerkit.DeployPreflight{Edge: cloudfront.Kind, Plan: providerkit.DeployPlan{Apps: apps}}); err != nil {
		t.Fatalf("preflight behind %s = %v, want it to pass: that edge reaches an origin by URL with the class's secret", cloudfront.Kind, err)
	}
	if err := refuseContainersBehindFunctionEdge(providerkit.DeployPreflight{Edge: cloudflare.Kind, Plan: providerkit.DeployPlan{Apps: apps}}); err == nil {
		t.Fatalf("preflight behind %s passed, want the container app refused: that edge presents no origin secret, so the front would answer it 404", cloudflare.Kind)
	}
	if err := refuseContainersBehindFunctionEdge(providerkit.DeployPreflight{Edge: apigateway.Kind, Plan: providerkit.DeployPlan{Apps: apps[1:]}}); err != nil {
		t.Fatalf("preflight of serverless apps behind %s = %v, want it to pass", apigateway.Kind, err)
	}
}
