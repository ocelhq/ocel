package vps_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

func TestVPSProvider(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		Spec:    providerkit.Spec{Version: "test", New: vps.New},
		Options: providerkit.Options{"ssh": map[string]any{"host": "203.0.113.10"}},
		Binary:  buildProvider(t),
	})
}

func TestTheRootCarriesTheVendorAndNoOptionalSetYet(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})

	if p.Vendor() != vps.Vendor {
		t.Errorf("Vendor() = %q, want %q", p.Vendor(), vps.Vendor)
	}
	if got := p.Serves(); len(got) != 0 {
		t.Errorf("Serves() = %v, want nothing until the provider provisions links of its own", got)
	}

	var root providerkit.Provider = p
	for name, held := range map[string]bool{
		"Warmer":            held[providerkit.Warmer](root),
		"CodeEmbedder":      held[providerkit.CodeEmbedder](root),
		"StackInspector":    held[providerkit.StackInspector](root),
		"GrantVerifier":     held[providerkit.GrantVerifier](root),
		"MembraneCrosser":   held[providerkit.MembraneCrosser](root),
		"DeployPreflighter": held[providerkit.DeployPreflighter](root),
		"MembraneSource":    held[providerkit.MembraneSource](root),
		"ArtifactPacker":    held[providerkit.ArtifactPacker](root),
		"Certifier":         held[providerkit.Certifier](root),
	} {
		if held {
			t.Errorf("the root carries %s, which the optional-set tier must be told to expect on it", name)
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
		t.Fatalf("build the vps provider: %v\n%s", err, out)
	}
	return binary
}
