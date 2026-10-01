package aws_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func TestAWSProvider(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		Server:  providerserver.Config{Version: "test", New: aws.New},
		Options: provider.Options{"region": "us-east-1"},
		Binary:  buildProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind: edges.DefaultKind,
		},
	})
}

func TestTheRouterRegistryOpensTheRouterEveryEdgePairsWith(t *testing.T) {
	t.Parallel()

	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
	conformance.RunRouters(t, p.Facts(), p.Edges(), p.Routers())
}

func TestTheProviderNamesTheVendorAndSetsEveryHookItImplements(t *testing.T) {
	t.Parallel()

	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	if p.Facts().Vendor != aws.Vendor {
		t.Errorf("Facts().Vendor = %q, want %q", p.Facts().Vendor, aws.Vendor)
	}
	for _, want := range []provider.BindingType{provider.BindingPostgres, provider.BindingBucket} {
		if !slices.Contains(p.Facts().Bindings, want) {
			t.Errorf("Facts().Bindings = %v, want it to include %s", p.Facts().Bindings, want)
		}
	}

	hooks := p.Hooks()
	for name, set := range map[string]bool{
		"PreflightDeploy":     hooks.PreflightDeploy != nil,
		"VerifyGrants":        hooks.VerifyGrants != nil,
		"InspectStack":        hooks.InspectStack != nil,
		"PackApp":             hooks.PackApp != nil,
		"EmbedCode":           hooks.EmbedCode != nil,
		"WarmFunctions":       hooks.WarmFunctions != nil,
		"ProgramEdge":         hooks.ProgramEdge != nil,
		"EnsureImageRegistry": hooks.EnsureImageRegistry != nil,
		"OpenRegistryImages":  hooks.OpenRegistryImages != nil,
		"ProveIdentity":       hooks.ProveIdentity != nil,
		"Cost":                hooks.Cost != nil,
	} {
		if !set {
			t.Errorf("the aws provider's hooks leave %s nil, so the deploy skips a step this provider implements", name)
		}
	}
}

const prebuiltProviderEnv = "OCEL_AWS_PROVIDER_BINARY"

func buildProvider(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv(prebuiltProviderEnv); prebuilt != "" {
		return prebuilt
	}
	binary := filepath.Join(t.TempDir(), "deploy")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/deploy")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the aws provider: %v\n%s", err, out)
	}
	return binary
}

func TestTopicsTasksAndWorkersAreRefusedAtPreflightAsUnsupported(t *testing.T) {
	t.Parallel()

	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
	conformance.RunWorkers(t, p.Facts())
}

func TestKVStoresAreRefusedAtPreflightAsUnsupported(t *testing.T) {
	t.Parallel()

	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)
	conformance.RunKVStores(t, p.Facts())
}
