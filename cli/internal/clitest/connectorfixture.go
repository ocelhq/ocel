package clitest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const FakeConnectorBinary = "#!/bin/sh\necho fake connector\n"

func SetUpConnectorFixture(t *testing.T, fingerprint, hostname string) FakeProject {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}

	t.Setenv(providerclient.ReadyTimeoutEnvVar, "5s")

	root := t.TempDir()
	WriteFile(t, filepath.Join(root, "ocel.fake.json"), `{
  "slug": "`+FixtureSlug+`",
  "provider": { "fake": {} }
}
`)

	p := fake.NewForProject(fake.Options{}, root)
	p.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: fingerprint, Hostname: hostname, OS: "linux", Arch: "amd64"})
	requests := ServeFake(t, p)
	InstallConnector(t, string(fake.Vendor), providers.Platform{GOOS: "linux", GOARCH: "amd64"}, []byte(FakeConnectorBinary))
	return FakeProject{Root: root, Provider: p, Requests: requests}
}

func InstallConnector(t *testing.T, name string, platform providers.Platform, content []byte) string {
	t.Helper()

	dir := os.Getenv(providers.OverrideEnvVar)
	if dir == "" {
		dir = t.TempDir()
		t.Setenv(providers.OverrideEnvVar, dir)
	}
	binary := filepath.Join(dir, string(providers.KindConnector), name, version.Version, platform.Dir())
	if err := os.MkdirAll(binary, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binary, err)
	}
	dest := filepath.Join(binary, providers.ExecutableName(providers.KindConnector, name, platform.GOOS))
	if err := os.WriteFile(dest, content, 0o755); err != nil {
		t.Fatalf("install the %s connector at %s: %v", name, dest, err)
	}
	return dest
}
