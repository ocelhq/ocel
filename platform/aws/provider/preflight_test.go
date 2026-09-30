package aws

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/router"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAnAppIsRefusedBehindAnEdgeThatPairsNoRouterWithItsCompute(t *testing.T) {
	t.Parallel()

	apps := []provider.AppEntry{
		{App: "web", Manifest: &contractv1.ManifestApp{Name: "web", Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}},
		{App: "api", Manifest: &contractv1.ManifestApp{Name: "api", Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}}}},
	}
	facts := (&Provider{}).Facts()
	err := refuseUnpairedApps(facts, provider.DeployPreflight{Edge: apigateway.Kind, Deploy: provider.DeploySpec{Apps: apps}})
	if err == nil || !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), string(apigateway.Kind)) || !strings.Contains(err.Error(), string(cloudfront.Kind)) {
		t.Fatalf("preflight behind %s = %v, want the container app refused by name, pointing at an edge that reaches containers: that edge invokes a function and would fail at promote otherwise", apigateway.Kind, err)
	}
	for _, front := range []edge.Kind{cloudfront.Kind, cloudflare.Kind} {
		if err := refuseUnpairedApps(facts, provider.DeployPreflight{Edge: front, Deploy: provider.DeploySpec{Apps: apps}}); err != nil {
			t.Errorf("preflight of a project mixing serverless and container apps behind %s = %v, want it to pass", front, err)
		}
	}
	if err := refuseUnpairedApps(facts, provider.DeployPreflight{Edge: apigateway.Kind, Deploy: provider.DeploySpec{Apps: apps[1:]}}); err != nil {
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

func TestTheArchitectureContainersAreBuiltForIsOneTheProviderShipsARuntimeFor(t *testing.T) {
	t.Parallel()

	p := &Provider{}
	for declared, want := range map[string]string{"": "amd64", arch.X8664: "amd64", arch.ARM64: "arm64"} {
		runs, err := p.Runtime().Arch(context.Background(), "web", declared)
		if err != nil || runs != want {
			t.Fatalf("ContainerArch(%q) = %q, %v, want %s: the image is built for the architecture the app's task runs on", declared, runs, err, want)
		}
		binary, err := p.Runtime().Binary(context.Background(), runs)
		if err != nil || len(binary) == 0 {
			t.Fatalf("ContainerRuntime(%s) = %d bytes, %v, want the runtime every container boots through", runs, len(binary), err)
		}
	}
	if _, err := p.Runtime().Arch(context.Background(), "web", "riscv64"); err == nil {
		t.Error("ContainerArch(riscv64) named an architecture, and no Fargate task runs on it")
	}
}

func TestAStaleEdgeKeyAndOriginSecretAreWarningsBeforeADeploy(t *testing.T) {
	p := NewProvider(Options{}, nil, aws.Config{}, defaultNamespace)
	if _, err := p.params.resolve(tierEdge{tier: environment.TierProduction, kind: cloudflare.Kind}, func() (bootstrap.TierParams, error) {
		return bootstrap.TierParams{
			EdgeCredentials: bootstrap.EdgeCredentials{AccessKeyID: "AKOLD", CreatedAt: time.Now().Add(-2 * bootstrap.EdgeKeyMaxAge)},
			OriginSecret:    bootstrap.OriginSecret{Current: "s1", CreatedAt: time.Now().Add(-2 * bootstrap.OriginSecretMaxAge)},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var progress fake.Log
	pre := provider.DeployPreflight{
		Deploy:   provider.DeploySpec{Tier: environment.TierProduction},
		Edge:     cloudflare.Kind,
		Progress: &progress,
	}

	if err := p.nagStaleEdgeKey(context.Background(), pre); err != nil {
		t.Fatal(err)
	}
	if err := p.nagStaleOriginSecret(context.Background(), pre); err != nil {
		t.Fatal(err)
	}
	lines := progress.Lines()
	if len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "WARN The production edge signs into this account with access key AKOLD") ||
		!strings.HasPrefix(lines[1], "WARN The production origin secret") {
		t.Errorf("preflight said %q, want both ageing credentials warned about, each naming its tier", lines)
	}
}

func TestACertificatePinTheEdgeIgnoresIsAWarning(t *testing.T) {
	const host = "shop.app.com"
	p := NewProvider(Options{Certificates: map[string]string{host: "arn:aws:acm:us-east-1:111122223333:certificate/pinned"}}, nil, aws.Config{}, defaultNamespace)
	var progress fake.Log

	if _, err := p.certificatesFor(cloudflare.Kind, router.Kind(cloudflare.Kind), host, &progress); err != nil {
		t.Fatalf("certificatesFor() = %v", err)
	}
	lines := progress.Lines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "WARN The certificate pinned for "+host+" is ignored") {
		t.Errorf("certificatesFor() said %q, want the ignored pin warned about", lines)
	}
}
