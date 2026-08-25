package clitest

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/provision"
)

func NewSession() session.Session {
	return session.Session{
		LoadCredentials:     credentials.Load,
		FetchProjectConfig:  provision.FetchProjectConfig,
		BuildApp:            appbuilder.Build,
		CollectAppFunctions: appbuilder.CollectFunctions,
		DeploymentID:        appbuilder.DeploymentID,
		DiscoverPRNumber:    func() string { return os.Getenv("OCEL_PR_NUMBER") },
		StdinIsTerminal:     func(io.Reader) bool { return false },
		StdoutIsTerminal:    deployui.IsTerminal,
		ConfigPath:          func() string { return os.Getenv("OCEL_CONFIG") },
		Verbose:             func() bool { return false },
		Format:              func() deployui.Format { return deployui.FormatHuman },
		Interrupt: func(ctx context.Context, _ io.Writer) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
	}
}

func IsolateConfigHome() func() {
	dir, err := os.MkdirTemp("", "ocel-cli-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Unsetenv("OCEL_CONFIG")
	return func() { os.RemoveAll(dir) }
}

func SetUpDeployFixture(t *testing.T) (root, sockPath string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}

	t.Setenv(provider.ReadyTimeoutEnvVar, "5s")

	root = t.TempDir()
	WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
	WriteFile(t, filepath.Join(root, "ocel", "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
const stack = new Error().stack ?? "";
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      resource: { type: "LINK_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
      stack,
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
	if err := os.Symlink(testBinary, filepath.Join(binDir, "deploy")); err != nil {
		t.Fatalf("symlink fake provider binary: %v", err)
	}

	sockPath = filepath.Join(t.TempDir(), "deploy-provider.sock")
	t.Setenv(FakeProviderEnvVar, "1")
	t.Setenv(fakeProviderSockEnvVar, sockPath)

	t.Setenv(FakeInfraClassEnvVar, "production")
	t.Setenv(FakeInfraPresentEnvVar, "1")

	return root, sockPath
}

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

func StubAppFunctions(sess *session.Session, functions []manifestbuilder.Function) {
	sess.BuildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, io.Writer) error {
		return nil
	}
	sess.CollectAppFunctions = func(string) ([]manifestbuilder.Function, error) {
		return functions, nil
	}
	StubRecordedDeploymentIDs(sess)
}

func StubRecordedDeploymentIDs(sess *session.Session) {
	sess.DeploymentID = func(_, app string) (string, error) { return FixtureDeploymentID(app), nil }
}

func WriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func WriteUsageMonorepo(t *testing.T, root string) {
	t.Helper()

	WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)
	WriteFile(t, filepath.Join(root, "ocel", "main.ts"), `
export * from "../shared/index.js";
`)
	WriteFile(t, filepath.Join(root, "shared", "declare.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}

function register(body: Record<string, unknown>) {
  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(
    fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  );
}

export function declarePostgres(name: string, stack: string) {
  register({ resource: { type: "LINK_TYPE_POSTGRES", name }, postgres: { version: "17" }, stack });
  return { name };
}

export function declareBucket(name: string, stack: string) {
  register({ resource: { type: "LINK_TYPE_BUCKET", name }, bucket: {}, stack });
  return { name };
}
`)
	WriteFile(t, filepath.Join(root, "shared", "db.ts"), `
import { declarePostgres } from "./declare.js";

export const db = declarePostgres("main", new Error().stack ?? "");
`)
	WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
`)
	WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db } from "../../../shared/index.js";

export function handler() {
  return db.name;
}
`)
}

func writeEdgeConfig(t *testing.T, root, declaration string) {
	t.Helper()

	WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
`+declaration+`};
`)
}

func ReadJournal(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read edge journal: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func SetUpEdgeFixture(t *testing.T, declaration string) (root, journal string, sess session.Session) {
	t.Helper()

	root, _ = SetUpDeployFixture(t)
	WriteUsageMonorepo(t, root)
	writeEdgeConfig(t, root, declaration)

	journal = filepath.Join(t.TempDir(), "edge.journal")
	t.Setenv(fakeEdgeJournalEnvVar, journal)

	sess = NewSession()
	SetLoggedIn(&sess)
	StubAppFunctions(&sess, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	return root, journal, sess
}

func SetLoggedIn(sess *session.Session) {
	sess.LoadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
}
