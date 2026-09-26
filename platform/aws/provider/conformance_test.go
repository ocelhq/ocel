package aws_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func TestAWSProvider(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		Spec:    providerkit.Spec{Version: "test", New: aws.New},
		Options: provider.Options{"region": "us-east-1"},
		Binary:  buildProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind: edges.DefaultKind,
		},
	})
}

func TestTheProviderCarriesTheVendorAndSetsEveryHookItImplements(t *testing.T) {
	t.Parallel()

	p := aws.NewProvider(aws.Options{Region: "us-east-1"}, nil, awssdk.Config{Region: "us-east-1"}, defaultNamespace)

	if p.Facts().Vendor != aws.Vendor {
		t.Errorf("Facts().Vendor = %q, want %q", p.Facts().Vendor, aws.Vendor)
	}
	for _, want := range []provider.BindingType{provider.BindingPostgres, provider.BindingBucket} {
		if !slices.Contains(p.Facts().Bindings, want) {
			t.Errorf("Facts().Bindings = %v, want it to carry %s", p.Facts().Bindings, want)
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
		"Cost":                hooks.Cost != nil,
	} {
		if !set {
			t.Errorf("the aws provider's hooks leave %s nil, so the deploy skips a step this provider implements", name)
		}
	}
}

func buildProvider(t *testing.T) string {
	t.Helper()
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
