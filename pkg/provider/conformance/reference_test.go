package conformance_test

import (
	"context"
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
	var vendor *fake.Provider
	conformance.Run(t, conformance.Suite{
		New: func(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
			p, err := fake.New(ctx, settings)
			vendor, _ = p.(*fake.Provider)
			return p, err
		},
		Server:  providerserver.Config{Version: "test", New: fake.New},
		Options: provider.Options{"region": "nowhere"},
		Binary:  buildFakeProvider(t),
		Certificates: &conformance.CertificateChecks{
			Kind: fake.KindRelay,
		},
		Logs: &conformance.LogChecks{Seed: func(_ *testing.T, target provider.LogTarget, entries []provider.LogEntry) {
			vendor.FakeLogs().Append(target.Physical(), entries...)
		}},
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
