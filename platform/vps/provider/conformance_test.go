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
		New:     vps.New,
		Spec:    providerkit.Spec{Version: "test", New: vps.New},
		Options: providerkit.Options{"host": "203.0.113.10"},
		Binary:  buildProvider(t),
	})
}

func buildProvider(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "ocelvps")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/ocelvps")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the vps provider: %v\n%s", err, out)
	}
	return binary
}
