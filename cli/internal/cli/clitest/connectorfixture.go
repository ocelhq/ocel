package clitest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/cli/internal/version"
)

const FakeConnectorBinary = "#!/bin/sh\necho fake connector\n"

func SetUpConnectorFixture(t *testing.T, fingerprint, hostname string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}

	t.Setenv(providerclient.ReadyTimeoutEnvVar, "5s")

	root := t.TempDir()
	WriteFile(t, filepath.Join(root, "ocel.vps.json"), `{
  "slug": "`+FixtureSlug+`",
  "provider": { "vps": { "ssh": "`+hostname+`" } }
}
`)

	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	InstallProvider(t, "vps", func(dest string) error { return os.Symlink(testBinary, dest) })
	InstallConnector(t, "vps", providers.Platform{GOOS: "linux", GOARCH: "amd64"}, []byte(FakeConnectorBinary))

	t.Setenv(FakeProviderEnvVar, "1")
	t.Setenv(fakeProviderSockEnvVar, filepath.Join(t.TempDir(), "connector-provider.sock"))
	t.Setenv(FakeConnectorTargetEnvVar, fingerprint)
	t.Setenv(FakeConnectorHostEnvVar, hostname)
	t.Setenv(FakeConnectorArchEnvVar, "amd64")
	t.Setenv(FakeConnectorLogEnvVar, filepath.Join(t.TempDir(), "connector.json"))

	return root
}

func InstallConnector(t *testing.T, name string, platform providers.Platform, content []byte) string {
	t.Helper()

	dir := os.Getenv(providers.OverrideEnvVar)
	if dir == "" {
		dir = t.TempDir()
		t.Setenv(providers.OverrideEnvVar, dir)
	}
	held := filepath.Join(dir, string(providers.KindConnector), name, version.Version, platform.Dir())
	if err := os.MkdirAll(held, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", held, err)
	}
	dest := filepath.Join(held, providers.ExecutableName(providers.KindConnector, name, platform.GOOS))
	if err := os.WriteFile(dest, content, 0o755); err != nil {
		t.Fatalf("install the %s connector at %s: %v", name, dest, err)
	}
	return dest
}
