package aws

import (
	"context"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAContainerAppIsRefusedBehindEveryEdgeButTheDefault(t *testing.T) {
	t.Parallel()

	apps := []provider.AppEntry{
		{App: "web", Manifest: &contractv1.ManifestApp{Name: "web", Compute: string(provider.ComputeContainer)}},
		{App: "api", Manifest: &contractv1.ManifestApp{Name: "api", Compute: string(provider.ComputeServerless)}},
	}
	err := refuseContainersBehindFunctionEdge(provider.DeployPreflight{Edge: apigateway.Kind, Plan: provider.DeployPlan{Apps: apps}})
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), string(apigateway.Kind)) {
		t.Fatalf("preflight behind %s = %v, want the container app refused by name: that edge invokes a function and would fail at promote otherwise", apigateway.Kind, err)
	}
	if err := refuseContainersBehindFunctionEdge(provider.DeployPreflight{Edge: cloudfront.Kind, Plan: provider.DeployPlan{Apps: apps}}); err != nil {
		t.Fatalf("preflight behind %s = %v, want it to pass: that edge reaches an origin by URL with the class's secret", cloudfront.Kind, err)
	}
	if err := refuseContainersBehindFunctionEdge(provider.DeployPreflight{Edge: cloudflare.Kind, Plan: provider.DeployPlan{Apps: apps}}); err == nil {
		t.Fatalf("preflight behind %s passed, want the container app refused: that edge presents no origin secret, so the front would answer it 404", cloudflare.Kind)
	}
	if err := refuseContainersBehindFunctionEdge(provider.DeployPreflight{Edge: apigateway.Kind, Plan: provider.DeployPlan{Apps: apps[1:]}}); err != nil {
		t.Fatalf("preflight of serverless apps behind %s = %v, want it to pass", apigateway.Kind, err)
	}
}

func TestAPublicBucketIsRefusedOnAws(t *testing.T) {
	t.Parallel()

	pre := provider.DeployPreflight{Resources: []provider.Resource{
		{Name: "avatars", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{Public: true}},
		{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
	}}

	err := refusePublicBuckets(pre)
	if err == nil || !strings.Contains(err.Error(), "avatars") {
		t.Fatalf("preflight = %v, want the public bucket refused by name", err)
	}

	private := provider.DeployPreflight{Resources: pre.Resources[1:]}
	if err := refusePublicBuckets(private); err != nil {
		t.Fatalf("preflight of a private bucket = %v, want it to pass", err)
	}
}

func TestTheArchitectureContainersAreBuiltForIsOneTheProviderCarriesARuntimeFor(t *testing.T) {
	t.Parallel()

	p := &Provider{}
	for declared, want := range map[string]string{"": "amd64", arch.X8664: "amd64", arch.ARM64: "arm64"} {
		runs, err := p.Runtime().Arch(context.Background(), "web", declared)
		if err != nil || runs != want {
			t.Fatalf("ContainerArch(%q) = %q, %v, want %s: the image is built for the architecture the app's task is stood up on", declared, runs, err, want)
		}
		held, err := p.Runtime().Binary(context.Background(), runs)
		if err != nil || len(held) == 0 {
			t.Fatalf("ContainerRuntime(%s) = %d bytes, %v, want the runtime every container boots through", runs, len(held), err)
		}
	}
	if _, err := p.Runtime().Arch(context.Background(), "web", "riscv64"); err == nil {
		t.Error("ContainerArch(riscv64) named an architecture, and no Fargate task runs on it")
	}
}
