package aws

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/router"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

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
