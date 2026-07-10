package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/declare"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestConfirmDeploy(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase y", "y\n", true},
		{"word yes", "yes\n", true},
		{"uppercase YES", "YES\n", true},
		{"explicit no", "n\n", false},
		{"empty answer defaults to no", "\n", false},
		{"unrecognized answer defaults to no", "sure\n", false},
		{"no input at all defaults to no", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got, err := confirmDeploy("proj_123", "@ocel/provider-aws", &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmDeploy() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmDeploy(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), "Deploy proj_123 with @ocel/provider-aws? [y/N]") {
				t.Errorf("stdout = %q, want it to contain the confirm prompt", stdout.String())
			}
		})
	}
}

func TestToDeclarations_MapsResourceFields(t *testing.T) {
	resources := []declare.Resource{
		{
			Name:     "main",
			Type:     resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
			Postgres: &resourcesv1.PostgresConfig{Version: "17"},
		},
	}

	decls := toDeclarations(resources)

	if len(decls) != 1 {
		t.Fatalf("len(decls) = %d, want 1", len(decls))
	}
	d := decls[0]
	if d.ID != "main" {
		t.Errorf("ID = %q, want %q", d.ID, "main")
	}
	if d.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
		t.Errorf("Type = %v, want RESOURCE_TYPE_POSTGRES", d.Type)
	}
	if d.Postgres.GetVersion() != "17" {
		t.Errorf("Postgres.Version = %q, want %q", d.Postgres.GetVersion(), "17")
	}
}

func TestRunDeploy_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runDeploy(context.Background(), t.TempDir(), deployOptions{}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runDeploy err = %v (%T), want *ExitError", err, err)
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunDeploy_MissingConfig_ErrorsBeforeAnySpawn(t *testing.T) {
	setLoggedIn(t)

	err := runDeploy(context.Background(), t.TempDir(), deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %v, want it to hint at `ocel init`", err)
	}
}

func TestRunDeploy_MalformedConfig_ErrorsBeforeAnySpawn(t *testing.T) {
	setLoggedIn(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `this is not valid TypeScript {{{`)

	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel.config.ts") {
		t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
	}
}

func TestRunDeploy_NoProviderConfigured_ErrorsBeforeAnySpawn(t *testing.T) {
	setLoggedIn(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  projectId: "proj_no_provider",
};
`)

	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy err = nil, want error")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Fatalf("err = %v, want it to mention the missing provider", err)
	}
}

// TestRunDeploy_HappyPath_DiscoversBuildsSpawnsAndDeploysToSuccess drives
// runDeploy end to end through the real discover -> collect -> build ->
// locate -> spawn -> deploy -> stream -> teardown wiring, against a fake
// provider binary (this test binary re-exec'd, see
// deploy_fakeprovider_test.go) resolved through the real
// cli/internal/providerlocator convention.
func TestRunDeploy_HappyPath_DiscoversBuildsSpawnsAndDeploysToSuccess(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if !strings.Contains(stdout.String(), "provisioning...") {
		t.Errorf("stdout = %q, want it to contain the streamed progress event", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deploy succeeded") {
		t.Errorf("stdout = %q, want a terminal success message", stdout.String())
	}
	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("stdout = %q, want the confirm prompt skipped by --yes", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

// TestRunDeploy_ConfirmSkippedWhenStdinNotATTY_ProceedsWithoutPrompting
// covers the non-interactive half of the confirm-prompt requirement: even
// with --yes omitted, a non-TTY stdin (as in every test, and in CI) must
// not block or prompt — it proceeds straight through to deploy. The
// interactive "shown on a real TTY" half isn't exercised here, consistent
// with how isTTY/isReaderTTY's real-terminal branch isn't unit-tested
// elsewhere in this package.
func TestRunDeploy_ConfirmSkippedWhenStdinNotATTY_ProceedsWithoutPrompting(t *testing.T) {
	root, sockPath := setUpDeployFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: false}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if strings.Contains(stdout.String(), "[y/N]") {
		t.Errorf("stdout = %q, want the confirm prompt skipped for non-TTY stdin", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deploy succeeded") {
		t.Errorf("stdout = %q, want deploy to still proceed to success", stdout.String())
	}

	waitForNoStaleSocket(t, sockPath)
}

// setUpDeployFixture writes a project (ocel.config.ts declaring a provider,
// and an ocel/main.ts discovery script declaring a single postgres resource
// "main") and a fake provider binary resolvable via the real
// providerlocator convention (a symlink to this re-exec'd test binary under
// node_modules/@ocel/provider-aws-<platform>-<arch>/bin/ocelaws). It logs the
// caller in, shortens the readiness timeout, and restores every package-level
// seam it touches via t.Cleanup. It returns the project root and the Unix
// socket path the fake provider will bind, for post-teardown assertions.
func setUpDeployFixture(t *testing.T) (root, sockPath string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}

	setLoggedIn(t)

	prevTimeout := deployReadyTimeout
	deployReadyTimeout = 5 * time.Second
	t.Cleanup(func() { deployReadyTimeout = prevTimeout })

	root = t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  projectId: "proj_deploy_happy",
  provider: { package: "@ocel/provider-aws", options: {} },
};
`)
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`)

	binDir := filepath.Join(root, "node_modules", "@ocel", "provider-aws-"+nodePlatformSuffix(t), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	if err := os.Symlink(testBinary, filepath.Join(binDir, "ocelaws")); err != nil {
		t.Fatalf("symlink fake provider binary: %v", err)
	}

	sockPath = filepath.Join(t.TempDir(), "deploy-provider.sock")
	t.Setenv(deployFakeProviderEnvVar, "1")
	t.Setenv(deployFakeProviderSockEnvVar, sockPath)

	return root, sockPath
}

// nodePlatformSuffix mirrors resolve-provider.cjs's platform/arch naming
// (see cli/internal/providerlocator/locator_test.go's hostPlatformSuffix),
// translated from Go's GOOS/GOARCH, so the fixture's node_modules layout is
// exactly what Locate's Node resolver expects on this host.
func nodePlatformSuffix(t *testing.T) string {
	t.Helper()

	nodePlatform := map[string]string{"darwin": "darwin", "linux": "linux"}[runtime.GOOS]
	if nodePlatform == "" {
		t.Skipf("no node platform mapping for GOOS=%s", runtime.GOOS)
	}
	nodeArch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if nodeArch == "" {
		t.Skipf("no node arch mapping for GOARCH=%s", runtime.GOARCH)
	}
	return nodePlatform + "-" + nodeArch
}

// waitForNoStaleSocket fails the test if sockPath still exists shortly
// after runDeploy returned — by then its deferred runner.Close() teardown
// should have removed it.
func waitForNoStaleSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("stale socket file left behind at %s", sockPath)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
