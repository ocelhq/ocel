package clitest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/appimages"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

func NewDeps() cmddeps.Deps {
	return cmddeps.Deps{
		LoadCredentials:     credentials.Load,
		FetchAccount:        resolve.StubAccount,
		BuildApp:            appbuilder.Build,
		RequireImageBuilder: appimages.RequireBuilder,
		BuildAppImages:      appimages.Build,
		CollectAppFunctions: appbuilder.CollectFunctions,
		DeploymentID:        appbuilder.DeploymentID,
		CollectDeclarations: deploycollector.Collect,
		ServeVarsUI:         envwire.ServeVarsUI,
		DiscoverPRNumber:    func() string { return os.Getenv("OCEL_PR_NUMBER") },
		StdinIsTerminal:     func(io.Reader) bool { return false },
		ConfigPath:          func() string { return os.Getenv("OCEL_CONFIG") },
		Presentation:        func(io.Writer) runui.Presentation { return runui.Resolve(runui.Origin{}) },
		Interrupt: func(ctx context.Context, _ io.Writer) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
	}
}

func WritePrebuiltFunction(t *testing.T, root, app, route string) {
	t.Helper()
	dir := filepath.Join(root, ".ocel", "output", "apps", app, "functions", route+".func")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]string{
		"runtime":   "nodejs24.x",
		"handler":   "index.handler",
		"framework": "express",
		"app":       app,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o644); err != nil {
		t.Fatal(err)
	}
}

func WaitForNoStaleSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sockPath); errors.Is(err, fs.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("stale socket file left behind at %s", sockPath)
			return
		}
		time.Sleep(10 * time.Millisecond)
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
  slug: "`+FixtureSlug+`",
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

	binDir := filepath.Join(root, "node_modules", "@ocel", "provider-aws-"+NodePlatformSuffix(t), "bin")
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

	t.Setenv(FakeInfraTierEnvVar, "production")
	t.Setenv(FakeInfraPresentEnvVar, "1")

	return root, sockPath
}

func NodePlatformSuffix(t *testing.T) string {
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

func StubBuild(deps *cmddeps.Deps, functions []manifestbuilder.Function) {
	deps.BuildApp = func(context.Context, *projectconfig.Config, map[string]map[string]string, string, io.Writer) error {
		return nil
	}
	deps.CollectAppFunctions = func(string) ([]manifestbuilder.Function, error) {
		return functions, nil
	}
	StubRecordedDeploymentIDs(deps)
}

const FixtureSlug = "test-app"

func FixtureImage(app string) string {
	sum := sha256.Sum256([]byte("ocel-test-image/" + app))
	return "ocel/" + FixtureSlug + "/" + app + "@sha256:" + hex.EncodeToString(sum[:])
}

func StubAppImages(deps *cmddeps.Deps, apps ...string) {
	refs := make(map[string]string, len(apps))
	for _, app := range apps {
		refs[app] = FixtureImage(app)
	}
	deps.RequireImageBuilder = func(context.Context, runui.Reporter, *projectconfig.Config) error { return nil }
	deps.BuildAppImages = func(context.Context, *projectconfig.Config, string, io.Writer) (map[string]string, error) {
		return refs, nil
	}
}

func StubRecordedDeploymentIDs(deps *cmddeps.Deps) {
	deps.DeploymentID = func(_, app string) (string, error) { return FixtureDeploymentID(app), nil }
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
  slug: "`+FixtureSlug+`",
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
  slug: "`+FixtureSlug+`",
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

func SetUpEdgeFixture(t *testing.T, declaration string) (root, journal string, deps cmddeps.Deps) {
	t.Helper()

	root, _ = SetUpDeployFixture(t)
	WriteUsageMonorepo(t, root)
	writeEdgeConfig(t, root, declaration)

	journal = filepath.Join(t.TempDir(), "edge.journal")
	t.Setenv(fakeEdgeJournalEnvVar, journal)

	deps = NewDeps()
	SetLoggedIn(&deps)
	StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	return root, journal, deps
}

func SetLoggedIn(deps *cmddeps.Deps) {
	deps.LoadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
}
