//go:build awslive

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

func TestRunDeploy_E2E_RealBuiltStubProvider(t *testing.T) {
	root, binPath := setUpRealProviderFixture(t)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

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

	body := getHealthWithRetry(t, fnURL, 3*time.Minute)
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("GET %s/health body = %q, want the express health route's {\"ok\":true}", fnURL, body)
	}

	sockPath := parseBoundSocketPath(t, stderr.String())
	waitForNoStaleSocket(t, sockPath)
	waitForNoOrphanProcess(t, binPath)
}

func setUpRealProviderFixture(t *testing.T) (root, binPath string) {
	t.Helper()

	repoRoot := requireRealProviderEnv(t)

	root = t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
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
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  apps: [{ name: %q, path: %q, framework: "express" }],
};
`, appName, filepath.ToSlash(appPath)))

	resourceModule, err := filepath.Rel(filepath.Join(root, "ocel"), filepath.Join(exampleDir, "ocel", "index"))
	if err != nil {
		t.Fatalf("compute resource module path: %v", err)
	}
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), fmt.Sprintf("export * from %q;\n", filepath.ToSlash(resourceModule)))

	binPath = installRealProvider(t, repoRoot, root)
	return root, binPath, appName
}

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

	prevTimeout := deployReadyTimeout
	deployReadyTimeout = 10 * time.Second
	t.Cleanup(func() { deployReadyTimeout = prevTimeout })

	return repoRoot
}

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
	build := exec.Command("go", "build", "-o", binPath, "github.com/ocelhq/ocel/platform/aws/provider/cmd/deploy")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cloud/aws: %v\n%s", err, out)
	}

	return binPath
}

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

var boundLineRE = regexp.MustCompile(`bound unix:(\S+)`)

func parseBoundSocketPath(t *testing.T, stderr string) string {
	t.Helper()
	m := boundLineRE.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("stderr = %q, want a line reporting the bound unix socket path", stderr)
	}
	return m[1]
}

func waitForNoOrphanProcess(t *testing.T, binPath string) {
	t.Helper()
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not found on PATH, cannot verify no orphaned provider process")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("pgrep", "-f", binPath).Output()
		if err != nil {
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

func parseFunctionURL(t *testing.T, stdout, logicalName string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(logicalName) + `:\s+(https?://\S+)`)
	m := re.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("stdout = %q, want a printed Function URL for %q", stdout, logicalName)
	}
	return strings.TrimRight(m[1], "/")
}

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
