package aws

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

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

func TestAContainerAppWithAFloorOfZeroIsRefusedOnAws(t *testing.T) {
	t.Parallel()

	container := &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Apps: []provider.AppEntry{
		{App: "api", Manifest: container, Instances: provider.Instances{Min: 1, Max: 4}},
		{App: "admin", Manifest: container, Instances: provider.Instances{Min: 0, Max: 2}},
	}}}

	err := refuseContainersScaledToZero(pre)
	if err == nil {
		t.Fatal("preflight let a container app scale to zero behind a load balancer that counts requests per task, and nothing would ever wake it")
	}
	for _, want := range []string{"admin", "instances.min", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("preflight = %q, want it to name %s", err, want)
		}
	}

	pre.Deploy.Apps = pre.Deploy.Apps[:1]
	if err := refuseContainersScaledToZero(pre); err != nil {
		t.Fatalf("preflight of a container app with a floor of one = %v, want it to pass", err)
	}
}

func TestAServerlessAppUsingAKVStoreIsNotRefusedByPreflight(t *testing.T) {
	t.Parallel()

	cache := provider.Resource{Name: "kv--cache", Type: provider.BindingKV}
	pre := provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Apps: []provider.AppEntry{
			{App: "web", Manifest: &contractv1.ManifestApp{}},
		}},
		Resources: []provider.Resource{cache},
		Apps:      []provider.AppUsage{{App: "web", Resources: []provider.Resource{cache}}},
	}

	var refused refusal.Refusal
	err := checkDeployOver(pre, account.TierParams{})
	if errors.As(err, &refused) && refused.Code == refusal.CodeUnsupported {
		t.Fatalf("preflight of a serverless app using a kv store = %v, want it let through: the app joins the VPC the store answers in", err)
	}
}

func TestAContainerAppSendingToATaskOrTopicPassesPreflight(t *testing.T) {
	t.Parallel()

	container := &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}
	resize := provider.Resource{Name: "topic--resize", Declared: "resize", Type: provider.BindingTask}
	pre := provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Infra: naming.InfraStack("production"), Apps: []provider.AppEntry{
			{App: "api", Manifest: container, Instances: provider.Instances{Min: 1, Max: 2}},
		}},
		Resources: []provider.Resource{resize},
		Apps: []provider.AppUsage{{
			App:       "api",
			Resources: []provider.Resource{resize},
			Grants:    []provider.Binding{{Name: "resize", Type: provider.BindingTask}},
		}},
	}

	var refused refusal.Refusal
	err := checkDeployOver(pre, account.TierParams{})
	if errors.As(err, &refused) && refused.Code == refusal.CodeUnsupported {
		t.Fatalf("preflight of a container app triggering a task = %v, want it to pass: its runtime serves the binding proxy", err)
	}
}

func TestAStaleEdgeKeyAndOriginSecretAreWarningsBeforeADeploy(t *testing.T) {
	params := account.TierParams{
		EdgeCredentials: account.EdgeCredentials{AccessKeyID: "AKOLD", CreatedAt: time.Now().Add(-2 * account.EdgeKeyMaxAge)},
		OriginSecret:    account.OriginSecret{Current: "s1", CreatedAt: time.Now().Add(-2 * account.OriginSecretMaxAge)},
	}
	var progress fake.Log
	pre := provider.DeployPreflight{
		Deploy:   provider.DeploySpec{Tier: environment.TierProduction},
		Edge:     cloudflare.Kind,
		Progress: &progress,
	}

	if err := checkTierParams(pre, params, time.Now()); err != nil {
		t.Fatal(err)
	}
	lines := progress.Lines()
	if len(lines) != 2 ||
		!strings.HasPrefix(lines[0], "WARN The production edge signs into this account with access key AKOLD") ||
		!strings.HasPrefix(lines[1], "WARN The production origin secret") {
		t.Errorf("preflight said %q, want both ageing credentials warned about, each naming its tier", lines)
	}
}

func TestAContainerAppThatOnlyHostsItsWorkerMayHaveAFloorOfZeroOnAws(t *testing.T) {
	t.Parallel()

	container := &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}
	media := provider.WorkerSpec{Name: "media", App: "media", Compute: provider.ComputeContainer}
	pre := provider.DeployPreflight{Deploy: provider.DeploySpec{Apps: []provider.AppEntry{
		{App: "media", Manifest: container, Instances: provider.Instances{Min: 0, Max: 3}, Workers: []provider.WorkerSpec{media}},
	}}}

	if err := refuseContainersScaledToZero(pre); err != nil {
		t.Fatalf("preflight of a worker app with no idle floor = %v, want it to pass: its services start on a send and a backlog, not on requests", err)
	}
}

func TestPreflightRefusesADeployOverAnUnreadableOriginSecret(t *testing.T) {
	refused := refusal.Refuse(refusal.CodeNotReady, "/ocel/origin/secret contains something other than the origin secret bootstrap writes")

	pre := provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction},
		Edge:   cloudflare.Kind,
	}
	if err := checkTierParams(pre, account.TierParams{OriginSecretErr: refused}, time.Now()); !errors.Is(err, refused) {
		t.Fatalf("preflight = %v, want the refusal: a deploy hands every release the secret the edge presents", err)
	}
}

func checkDeployOver(pre provider.DeployPreflight, params account.TierParams) error {
	if err := refusePublicBuckets(pre); err != nil {
		return err
	}
	if err := refuseContainersScaledToZero(pre); err != nil {
		return err
	}
	return checkTierParams(pre, params, time.Now())
}
