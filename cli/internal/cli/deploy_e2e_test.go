//go:build awslive

// This end-to-end test drives `ocel deploy` against the REAL built cloud/aws
// provider, which now performs real provisioning (Aurora Serverless v2, S3,
// CloudFormation). It must never run in CI — a successful deploy creates real
// billable AWS infrastructure — so it is gated behind the `awslive` build tag
// and run manually against a disposable account:
//
//	go test -tags awslive ./cli/internal/cli -run E2E
//
// In default CI the CLI<->provider chain is covered instead by the fake
// provider in deploy_fakeprovider_test.go (TestRunDeploy_HappyPath).
package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRunDeploy_E2E_RealBuiltStubProvider drives `ocel deploy` end to end
// against the REAL, BUILT cloud/aws provider binary (T7) — not the
// re-exec'd fake TestRunDeploy_HappyPath uses — proving the full discover
// -> collect -> build manifest -> locate -> spawn -> ready -> deploy ->
// stream -> teardown chain against an actual provider process, with the
// resource declared through the real "ocel/postgres" SDK import (T6's
// version-pass-through fix), not a hand-rolled fetch. This is the only test
// that exercises every piece together (ocelhq-x53.11 PRD, "End-to-end"
// testing decision).
func TestRunDeploy_E2E_RealBuiltStubProvider(t *testing.T) {
	root, binPath := setUpRealProviderFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	// The VALUE assertion: the stub provider's own log output must name the
	// exact typed version ("15", not the SDK's default "17") it decoded off
	// the wire, so this can't pass on a manifest that merely arrived
	// well-formed.
	if !strings.Contains(stdout.String(), "postgres_main: postgres version=15") {
		t.Errorf("stdout = %q, want the real stub provider to report the exact typed postgres version it decoded", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Deploy succeeded") {
		t.Errorf("stdout = %q, want a terminal success message", stdout.String())
	}

	sockPath := parseBoundSocketPath(t, stderr.String())
	waitForNoStaleSocket(t, sockPath)
	waitForNoOrphanProcess(t, binPath)
}

// setUpRealProviderFixture writes a project (ocel.config.ts declaring the
// AWS provider, and an ocel/main.ts that imports the real "ocel/postgres"
// SDK and declares a single postgres resource with a non-default version)
// and builds the real cloud/aws provider binary into the fixture's
// node_modules under the real @ocel/provider-aws-<platform>-<arch>
// convention, so providerlocator.Locate resolves it exactly as it would a
// real npm install. It returns the project root and the built binary's
// path.
func setUpRealProviderFixture(t *testing.T) (root, binPath string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket real provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found on PATH")
	}

	repoRoot := repoRootDir(t)
	ocelDist := filepath.Join(repoRoot, "packages", "ocel", "dist")
	if _, err := os.Stat(ocelDist); err != nil {
		t.Skipf("packages/ocel is not built (missing %s); run `pnpm --filter ocel build` first", ocelDist)
	}

	setLoggedIn(t)

	prevTimeout := deployReadyTimeout
	deployReadyTimeout = 10 * time.Second
	t.Cleanup(func() { deployReadyTimeout = prevTimeout })

	root = t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  projectId: "proj_deploy_e2e",
  provider: { package: "@ocel/provider-aws", options: {} },
};
`)
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
import { postgres } from "ocel/postgres";

postgres("main", { version: "15" });
`)

	// Resolve "ocel" exactly as a real project would: a symlink into the
	// built workspace package, so discovery bundles the actual SDK code
	// (including its node_modules for @connectrpc/connect, zod, pg, ...)
	// rather than anything test-specific.
	nodeModules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.Symlink(filepath.Join(repoRoot, "packages", "ocel"), filepath.Join(nodeModules, "ocel")); err != nil {
		t.Fatalf("symlink ocel package: %v", err)
	}

	binDir := filepath.Join(nodeModules, "@ocel", "provider-aws-"+nodePlatformSuffix(t), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	binPath = filepath.Join(binDir, "aws")
	build := exec.Command("go", "build", "-o", binPath, "github.com/ocelhq/ocel/cloud/aws/cmd/aws")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cloud/aws: %v\n%s", err, out)
	}

	return root, binPath
}

// repoRootDir returns the monorepo root (the directory containing
// go.work), derived from this file's own location rather than the test's
// working directory. Mirrors providerlocator_test.go's helper of the same
// name in its own package.
func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.work")); err != nil {
		t.Fatalf("computed repo root %q does not contain go.work: %v", dir, err)
	}
	return dir
}

// boundLineRE matches the real cloud/aws binary's own diagnostic stderr
// line ("ocel aws provider dev: bound unix:/tmp/ocel-provider-....sock"),
// letting the test recover the socket path providerrunner's Close() is
// expected to remove.
var boundLineRE = regexp.MustCompile(`bound unix:(\S+)`)

func parseBoundSocketPath(t *testing.T, stderr string) string {
	t.Helper()
	m := boundLineRE.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("stderr = %q, want a line reporting the bound unix socket path", stderr)
	}
	return m[1]
}

// waitForNoOrphanProcess fails the test if a process matching binPath's
// exact command line is still running shortly after runDeploy returned —
// by then providerrunner's teardown (SIGTERM, then SIGKILL after a grace
// period) should have reaped it. Skips (rather than fails) if pgrep isn't
// available, since that's an environment limitation, not a deploy bug.
func waitForNoOrphanProcess(t *testing.T, binPath string) {
	t.Helper()
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not found on PATH, cannot verify no orphaned provider process")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("pgrep", "-f", binPath).Output()
		if err != nil {
			// pgrep exits 1 when nothing matches.
			return
		}
		if strings.TrimSpace(string(out)) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("orphaned provider process still running for %s: pids %s", binPath, strings.TrimSpace(string(out)))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
