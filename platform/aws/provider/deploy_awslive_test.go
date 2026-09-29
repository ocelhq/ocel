//go:build awslive

package aws_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

func TestDeployThroughARealBuiltProviderServesTheAppItDeployed(t *testing.T) {
	t.Run("a real built provider reports the typed resource output it decoded", func(t *testing.T) {
		deploying := newRealDeploy(t)
		write(t, filepath.Join(deploying.project, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { aws: {} },
};
`)
		write(t, filepath.Join(deploying.project, "infra", "main.ts"), `
import { postgres } from "ocel/postgres";

postgres("main", { version: "15" });
`)

		stdout, stderr := deploying.deploy(t)
		if !strings.Contains(stdout, "db--main: postgres version=15") {
			t.Errorf("stdout = %q, want the real provider to report the exact typed postgres version it decoded", stdout)
		}
		if !strings.Contains(stdout, "Deployed") {
			t.Errorf("stdout = %q, want a terminal success message", stdout)
		}
		waitForNoStaleSocket(t, parseBoundSocketPath(t, stderr))
		waitForNoOrphanProcess(t, deploying.journey.installed())
	})

	t.Run("an express app answers on the function URL it was deployed to", func(t *testing.T) {
		deploying := newRealDeploy(t)
		fixtureDir := filepath.Join(repoRoot(t), "tests", "fixtures", "sdk", "node")
		if _, err := os.Stat(filepath.Join(fixtureDir, "node_modules")); err != nil {
			t.Skipf("tests/fixtures/sdk/node is not installed (missing %s); run `pnpm install` first", filepath.Join(fixtureDir, "node_modules"))
		}
		appPath, err := filepath.Rel(deploying.project, fixtureDir)
		if err != nil {
			t.Fatalf("compute app path: %v", err)
		}
		const appName = "api"
		write(t, filepath.Join(deploying.project, "ocel.config.ts"), fmt.Sprintf(`
export default {
  slug: "test-app",
  provider: { aws: {} },
  apps: [{ name: %q, path: %q, framework: "node" }],
};
`, appName, filepath.ToSlash(appPath)))
		resourceModule, err := filepath.Rel(filepath.Join(deploying.project, "infra"), filepath.Join(fixtureDir, "infra", "index"))
		if err != nil {
			t.Fatalf("compute resource module path: %v", err)
		}
		write(t, filepath.Join(deploying.project, "infra", "main.ts"), fmt.Sprintf("export * from %q;\n", filepath.ToSlash(resourceModule)))

		stdout, stderr := deploying.deploy(t)
		if !strings.Contains(stdout, "Deployed") {
			t.Fatalf("stdout = %q, want a terminal success message", stdout)
		}
		body := getHealthWithRetry(t, parseFunctionURL(t, stdout, appName), 3*time.Minute)
		if !strings.Contains(body, `"ok":true`) {
			t.Errorf("GET /health body = %q, want the express health route's {\"ok\":true}", body)
		}
		waitForNoStaleSocket(t, parseBoundSocketPath(t, stderr))
		waitForNoOrphanProcess(t, deploying.journey.installed())
	})
}

type realDeploy struct {
	journey journey
	project string
}

func newRealDeploy(t *testing.T) realDeploy {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
	root := repoRoot(t)
	ocelPackage := filepath.Join(root, "packages", "ocel")
	if _, err := os.Stat(filepath.Join(ocelPackage, "dist")); err != nil {
		t.Skipf("packages/ocel is not built (missing %s); run `pnpm --filter ocel build` first", filepath.Join(ocelPackage, "dist"))
	}

	dir := t.TempDir()
	deploying := realDeploy{
		journey: journey{
			bin:       filepath.Join(dir, "ocel"),
			settings:  filepath.Join(dir, "config"),
			cache:     filepath.Join(dir, "cache"),
			providers: filepath.Join(dir, "providers"),
		},
		project: filepath.Join(dir, "project"),
	}
	build(t, filepath.Join(root, "cli"), deploying.journey.bin, "./ocel")
	build(t, ".", deploying.journey.installed(), "./cmd/deploy")
	if err := os.MkdirAll(filepath.Join(deploying.project, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ocelPackage, filepath.Join(deploying.project, "node_modules", "ocel")); err != nil {
		t.Fatalf("symlink the ocel package: %v", err)
	}
	return deploying
}

func (d realDeploy) deploy(t *testing.T) (stdout, stderr string) {
	t.Helper()
	ctx, done := context.WithTimeout(context.Background(), patience)
	defer done()

	cmd := exec.CommandContext(ctx, d.journey.bin, "deploy", "--yes")
	cmd.Dir = d.project
	cmd.Env = append(d.journey.env(), "OCEL_READY_TIMEOUT=10s", "OCEL_PROVIDER_DEBUG=1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("ocel deploy --yes = %v; stdout=%s stderr=%s", err, out.String(), errOut.String())
	}
	return plain(out.String()), plain(errOut.String())
}

var boundLinePattern = regexp.MustCompile(`bound unix:(\S+)`)

func parseBoundSocketPath(t *testing.T, stderr string) string {
	t.Helper()
	m := boundLinePattern.FindStringSubmatch(stderr)
	if m == nil {
		t.Fatalf("stderr = %q, want a line reporting the bound unix socket path", stderr)
	}
	return m[1]
}

func waitForNoStaleSocket(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); errors.Is(err, fs.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("stale socket file left behind at %s", socketPath)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForNoOrphanProcess(t *testing.T, binary string) {
	t.Helper()
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not found on PATH, cannot verify no orphaned provider process")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("pgrep", "-f", binary).Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("orphaned provider process still running for %s: pids %s", binary, strings.TrimSpace(string(out)))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func parseFunctionURL(t *testing.T, stdout, logicalName string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(logicalName) + `:\s+(https?://\S+)`)
	m := pattern.FindStringSubmatch(stdout)
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
		switch {
		case readErr != nil:
			lastErr = readErr
		case resp.StatusCode == http.StatusOK:
			return string(body)
		default:
			lastStatus = resp.StatusCode
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("GET %s never returned 200 within %s (last status=%d, last err=%v)", healthURL, timeout, lastStatus, lastErr)
	return ""
}
