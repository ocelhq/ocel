package provider_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	provider "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func TestAWSProvider(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		Spec:    providerkit.Spec{Version: "test", New: provider.New},
		Options: providerkit.Options{"region": "us-east-1"},
		Binary:  buildProvider(t),
		Certifier: &conformance.CertifierChecks{
			Kind: edges.DefaultKind,
		},
	})
}

func TestTheRootCarriesTheVendorAndEveryOptionalSet(t *testing.T) {
	t.Parallel()

	p := provider.NewProvider(provider.Options{Region: "us-east-1"}, aws.Config{Region: "us-east-1"})

	if p.Vendor() != provider.Vendor {
		t.Errorf("Vendor() = %q, want %q", p.Vendor(), provider.Vendor)
	}
	for _, want := range []providerkit.LinkType{providerkit.LinkPostgres, providerkit.LinkBucket} {
		if !slices.Contains(p.Serves(), want) {
			t.Errorf("Serves() = %v, want it to carry %s", p.Serves(), want)
		}
	}

	var root providerkit.Provider = p
	for name, held := range map[string]bool{
		"Warmer":         held[providerkit.Warmer](root),
		"CodeEmbedder":   held[providerkit.CodeEmbedder](root),
		"StackInspector": held[providerkit.StackInspector](root),
		"MembraneSource": held[providerkit.MembraneSource](root),
		"ArtifactPacker": held[providerkit.ArtifactPacker](root),
		"GrantVerifier":  held[providerkit.GrantVerifier](root),
	} {
		if !held {
			t.Errorf("the root does not carry %s, and a wrapped port would never bring it back", name)
		}
	}
}

func held[T any](root providerkit.Provider) bool {
	_, ok := root.(T)
	return ok
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
