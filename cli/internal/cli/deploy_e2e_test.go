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
	"fmt"
	"io"
	"net/http"
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
// against the REAL, BUILT cloud/aws provider binary — not the re-exec'd fake
// TestRunDeploy_HappyPath uses — proving the full discover -> collect ->
// build manifest -> locate -> spawn -> ready -> deploy -> stream -> teardown
// chain against an actual provider process, with the resource declared
// through the real "ocel/postgres" SDK import, not a hand-rolled fetch. This
// is the only test that exercises every piece together.
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
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Errorf("stdout = %q, want a terminal success message", stdout.String())
	}

	sockPath := parseBoundSocketPath(t, stderr.String())
	waitForNoStaleSocket(t, sockPath)
	waitForNoOrphanProcess(t, binPath)
}

// TestRunDeploy_E2E_ExpressFunctionURL is the Seam C proof: it deploys the real
// examples/express app end to end against the REAL, BUILT cloud/aws provider —
// discover -> build the app into a manifest function -> realize a Lambda +
// public Function URL — then reads the printed Function URL back out of stdout
// and issues a real HTTP GET to the express app's own /health route. The value
// assertion is that the deployed app actually answers ({"ok":true}), not merely
// that a well-formed URL was printed, so it can only pass on infrastructure
// that was genuinely realized and is reachable.
func TestRunDeploy_E2E_ExpressFunctionURL(t *testing.T) {
	root, binPath, fnName := setUpRealProviderExpressFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Deployed") {
		t.Fatalf("stdout = %q, want a terminal success message", stdout.String())
	}

	fnURL := parseFunctionURL(t, stdout.String(), fnName)

	// The VALUE assertion: hit the real deployed Function URL and prove the
	// express app's own /health route answers. Bounded polling absorbs the
	// Lambda cold-start propagation a fresh Function URL needs.
	body := getHealthWithRetry(t, fnURL, 3*time.Minute)
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("GET %s/health body = %q, want the express health route's {\"ok\":true}", fnURL, body)
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

	repoRoot := requireRealProviderEnv(t)

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

	binPath = installRealProvider(t, repoRoot, root)
	return root, binPath
}

// setUpRealProviderExpressFixture writes a project that configures the real
// examples/express app (apps: [{ name, path, framework: "express" }]) against
// the AWS provider, so `ocel deploy` builds it into a manifest function the
// provider realizes as a Lambda + public Function URL. It reuses the same base
// as setUpRealProviderFixture (login, ocel symlink, built provider binary) but
// points the single app at the checked-in example rather than declaring
// infrastructure directly. It returns the project root, the built provider
// binary's path, and the deployed function's logical name (used to read its
// Function URL back out of deploy's printed outputs).
func setUpRealProviderExpressFixture(t *testing.T) (root, binPath, funcLogicalName string) {
	t.Helper()

	repoRoot := requireRealProviderEnv(t)

	exampleDir := filepath.Join(repoRoot, "examples", "express")
	if _, err := os.Stat(filepath.Join(exampleDir, "node_modules")); err != nil {
		t.Skipf("examples/express is not installed (missing %s); run `pnpm install` first", filepath.Join(exampleDir, "node_modules"))
	}

	root = t.TempDir()

	appPath, err := filepath.Rel(root, exampleDir)
	if err != nil {
		t.Fatalf("compute app path: %v", err)
	}

	const appName = "api"
	writeFile(t, filepath.Join(root, "ocel.config.ts"), fmt.Sprintf(`
export default {
  projectId: "proj_express_e2e",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [{ name: %q, path: %q, framework: "express" }],
};
`, appName, filepath.ToSlash(appPath)))

	// The express example resolves postgres("main") and bucket("uploads") at
	// module load, so the deployed Lambda only boots once those resources are
	// provisioned and their OCEL_RESOURCE_* env is injected onto the function
	// (see cloud/aws registerFunction). Re-export the example's own resource
	// declarations into the discovery path so the manifest carries them and the
	// app's cold start succeeds.
	resourceModule, err := filepath.Rel(filepath.Join(root, "ocel"), filepath.Join(exampleDir, "ocel", "index"))
	if err != nil {
		t.Fatalf("compute resource module path: %v", err)
	}
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), fmt.Sprintf("export * from %q;\n", filepath.ToSlash(resourceModule)))

	binPath = installRealProvider(t, repoRoot, root)
	return root, binPath, appName
}

// requireRealProviderEnv gates the awslive fixtures on the toolchain they need
// (a POSIX host with node + go and a built packages/ocel), signs the CLI in,
// and shortens the provider ready timeout for the run. It returns the monorepo
// root. It centralises what setUpRealProviderFixture and its express sibling
// share before either writes its project.
func requireRealProviderEnv(t *testing.T) string {
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

	return repoRoot
}

// installRealProvider resolves "ocel" exactly as a real project would — a
// symlink into the built workspace package, so discovery bundles the actual SDK
// code (including its node_modules for @connectrpc/connect, zod, pg, ...)
// rather than anything test-specific — then builds the real cloud/aws provider
// binary into the fixture's node_modules under the real
// @ocel/provider-aws-<platform>-<arch> convention, so providerlocator.Locate
// resolves it exactly as it would a real npm install. It returns the built
// binary's path.
func installRealProvider(t *testing.T, repoRoot, root string) (binPath string) {
	t.Helper()

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
	binPath = filepath.Join(binDir, "deploy")
	build := exec.Command("go", "build", "-o", binPath, "github.com/ocelhq/ocel/cloud/aws/cmd/deploy")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cloud/aws: %v\n%s", err, out)
	}

	return binPath
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

// parseFunctionURL recovers the deployed function's Function URL from deploy's
// printed connection outputs, where printResourceOutputs writes it as
// "  <logicalName>: <url>". It matches on the exact logical name so the
// postgres/bucket output lines (also printed) can't be mistaken for it, and
// trims the trailing slash so callers can append a route path cleanly.
func parseFunctionURL(t *testing.T, stdout, logicalName string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(logicalName) + `:\s+(https?://\S+)`)
	m := re.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("stdout = %q, want a printed Function URL for %q", stdout, logicalName)
	}
	return strings.TrimRight(m[1], "/")
}

// getHealthWithRetry polls baseURL+"/health" until it returns 200 or timeout
// elapses, returning the successful response body. A fresh Function URL can 5xx
// or refuse for a short while as the Lambda and its URL propagate, so a failed
// attempt is retried rather than fatal; only exhausting the timeout fails.
func getHealthWithRetry(t *testing.T, baseURL string, timeout time.Duration) string {
	t.Helper()
	healthURL := baseURL + "/health"
	deadline := time.Now().Add(timeout)
	var lastStatus int
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err != nil {
			lastErr = err
			time.Sleep(3 * time.Second)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			time.Sleep(3 * time.Second)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return string(body)
		}
		lastStatus = resp.StatusCode
		lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("GET %s never returned 200 within %s (last status=%d, last err=%v)", healthURL, timeout, lastStatus, lastErr)
	return ""
}
