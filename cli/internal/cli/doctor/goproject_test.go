package doctor

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/provider"
)

func TestDoctorPassesOnAGoProjectWithNoNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.24\n")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{
  "slug": "go-shop",
  "provider": { "name": "aws", "options": {} },
  "domains": { "production": "shop.example.com", "preview": "*.preview.acme.com" }
}
`)

	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	clitest.InstallProvider(t, "aws", func(dest string) error { return os.Symlink(testBinary, dest) })

	t.Setenv(provider.ReadyTimeoutEnvVar, "5s")
	t.Setenv(clitest.FakeProviderEnvVar, "1")
	t.Setenv("OCEL_TEST_DEPLOY_FAKE_PROVIDER_SOCK", filepath.Join(t.TempDir(), "deploy-provider.sock"))
	t.Setenv(clitest.FakeIDProviderEnvVar, "aws")
	t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
	t.Setenv(clitest.FakeBootstrapEnvVar, "current")
	t.Setenv(clitest.FakePreviewBootstrapEnvVar, "current")
	t.Setenv("PATH", t.TempDir())

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), deps, root, &stdout, &stderr); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "node") {
		t.Fatalf("a Go project was told about node:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Good to go.") {
		t.Fatalf("doctor did not pass on a Go project with no node:\n%s", stdout.String())
	}
}
