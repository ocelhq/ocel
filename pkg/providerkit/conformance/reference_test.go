package conformance_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func TestReferenceProvider(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		New:     fake.New,
		Spec:    providerkit.Spec{Version: "test", New: fake.New},
		Options: providerkit.Options{"region": "nowhere"},
		Binary:  buildFakeProvider(t),
		Certifier: &conformance.CertifierChecks{
			Kind: fake.KindRelay,
		},
	})
}

func buildFakeProvider(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fakeprovider")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./../fake/cmd/fakeprovider")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the fake provider: %v\n%s", err, out)
	}
	return binary
}
