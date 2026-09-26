package conformance_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func TestReferenceProvider(t *testing.T) {
	conformance.Run(t, conformance.Suite{
		New:     fake.New,
		Server:  providerserver.Config{Version: "test", New: fake.New},
		Options: provider.Options{"region": "nowhere"},
		Binary:  buildFakeProvider(t),
		Certificates: &conformance.CertificateChecks{
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
