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

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/projecteditor"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/providers"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func DiscoveryDir(root string) string {
	return filepath.Join(root, constants.DefaultDiscoveryDirName)
}

func AttachTerminalSink(deps cmddeps.Deps, w io.Writer) {
	deps.Events.Attach(runui.NewTerminalSink(deps.Presentation(w), w))
}

func NewDeps() cmddeps.Deps {
	return cmddeps.Deps{
		LoadCredentials:         console.LoadCredentials,
		SaveCredentials:         console.SaveCredentials,
		DeleteCredentials:       console.DeleteCredentials,
		BuildApps:               build.Apps,
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		ReadFunctions:           build.ReadFunctions,
		ProbePostgres:           fakeProbePostgres,
		ProbeBucket:             fakeProbeBucket,
		DeploymentID:            build.DeploymentID,
		CollectDeclarations:     declaration.Collect,
		ServeVariableEditor:     projecteditor.Serve,
		DiscoverPRNumber:        func() string { return os.Getenv("OCEL_PR_NUMBER") },
		StdinIsTerminal:         func(io.Reader) bool { return false },
		ConfigPath:              func() string { return os.Getenv("OCEL_CONFIG") },
		Presentation:            func(io.Writer) runui.Presentation { return runui.Resolve(runui.Origin{}) },
		Events:                  run.NewBus(time.Now),
	}
}

func WritePrebuiltFunction(t *testing.T, root, app, route string) {
	t.Helper()
	dir := filepath.Join(root, constants.ProjectStateDirName, "output", "apps", app, "functions", route+".func")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"framework": map[string]string{"name": "node"},
		"handler":   "index.handler",
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

func UnsetColorEnv() {
	for _, name := range []string{"FORCE_COLOR", "CLICOLOR_FORCE", "GITHUB_ACTIONS"} {
		os.Unsetenv(name)
	}
}

func SetUpDeployFixture(t *testing.T) (root, sockPath string) {
	t.Helper()

	root = writeProject(t)
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	InstallProvider(t, string(fake.Vendor), func(dest string) error { return os.Symlink(testBinary, dest) })

	sockPath = filepath.Join(t.TempDir(), "deploy-provider.sock")
	t.Setenv(FakeProviderEnvVar, "1")
	t.Setenv(fakeProviderSockEnvVar, sockPath)

	t.Setenv(FakeInfraTierEnvVar, "production")
	t.Setenv(FakeInfraPresentEnvVar, "1")

	return root, sockPath
}

func writeProject(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}

	t.Setenv(providerclient.ReadyTimeoutEnvVar, "5s")

	root := t.TempDir()
	WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+FixtureSlug+`",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)
	WriteFile(t, filepath.Join(DiscoveryDir(root), "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
const source = declarationSite();
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+` },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
      source,
    }),
  }),
);
function declarationSite(): string {
  const at = /\(?((?:\/|file:)[^()]+?):(\d+):\d+\)?$/.exec(((new Error().stack ?? "").split("\n")[2] ?? "").trim());
  return at ? at[1].replace(/^file:\/\//, "") + ":" + at[2] : "";
}
export {};
`)
	return root
}

func InstallProvider(t *testing.T, name string, place func(dest string) error) string {
	t.Helper()

	dir := os.Getenv(providers.OverrideEnvVar)
	if dir == "" {
		dir = t.TempDir()
		t.Setenv(providers.OverrideEnvVar, dir)
	}
	binary := filepath.Join(dir, string(providers.KindProvider), name, version.Version, runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.MkdirAll(binary, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binary, err)
	}
	dest := filepath.Join(binary, providers.ExecutableName(providers.KindProvider, name, runtime.GOOS))
	if err := place(dest); err != nil {
		t.Fatalf("install the %s provider at %s: %v", name, dest, err)
	}
	return dest
}

func StubBuild(deps *cmddeps.Deps, functions []build.Function) {
	deps.BuildApps = func(context.Context, *projectconfig.Config, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
		return build.Output{Functions: functions}, nil
	}
	deps.ReadPrebuilt = func(context.Context, *projectconfig.Config, map[string]string) (build.Output, error) {
		return build.Output{Functions: functions}, nil
	}
	deps.ReadFunctions = func(string) ([]build.Function, error) {
		return functions, nil
	}
	StubRecordedDeploymentIDs(deps)
}

const FixtureSlug = "test-app"

func AddFakeProviderIDs() {
	configdoc.AddKnownIDs(string(fake.Vendor), []string{string(fake.KindRelay), string(fake.KindDirect)}, []string{string(fake.KindZone)})
}

func FixtureImage(app string) string {
	sum := sha256.Sum256([]byte("ocel-test-image/" + app))
	return "ocel/" + FixtureSlug + "/" + app + "@sha256:" + hex.EncodeToString(sum[:])
}

func StubAppImages(deps *cmddeps.Deps, apps ...string) {
	refs := make(map[string]string, len(apps))
	for _, app := range apps {
		refs[app] = FixtureImage(app)
	}
	deps.RefuseUnbuildableImages = func(context.Context, *run.Span, *projectconfig.Config, map[string]string) error {
		return nil
	}
	buildApps := deps.BuildApps
	deps.BuildApps = func(ctx context.Context, cfg *projectconfig.Config, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error) {
		built, err := buildApps(ctx, cfg, env, archs, log)
		built.Images = refs
		return built, err
	}
	deps.ReadPrebuilt = func(_ context.Context, cfg *projectconfig.Config, _ map[string]string) (build.Output, error) {
		functions, err := deps.ReadFunctions(cfg.Dir)
		return build.Output{Functions: functions, Images: refs}, err
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
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)
	WriteFile(t, filepath.Join(DiscoveryDir(root), "main.ts"), `
export * from "../shared/index.js";
`)
	WriteFile(t, filepath.Join(root, "shared", "declare.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}

function register(body: Record<string, unknown>) {
  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(
    fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+` },
      body: JSON.stringify(body),
    }),
  );
}

function site(): string {
  const at = /\(?((?:\/|file:)[^()]+?):(\d+):\d+\)?$/.exec(((new Error().stack ?? "").split("\n")[3] ?? "").trim());
  return at ? at[1].replace(/^file:\/\//, "") + ":" + at[2] : "";
}

export function declarePostgres(name: string) {
  register({ resource: { type: "RESOURCE_TYPE_POSTGRES", name }, postgres: { version: "17" }, source: site() });
  return { name };
}

export function declareBucket(name: string) {
  register({ resource: { type: "RESOURCE_TYPE_BUCKET", name }, bucket: {}, source: site() });
  return { name };
}
`)
	WriteFile(t, filepath.Join(root, "shared", "db.ts"), `
import { declarePostgres } from "./declare.js";

export const db = declarePostgres("main");
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
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
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
	t.Setenv(FakeEdgeJournalEnvVar, journal)

	deps = NewDeps()
	SetLoggedIn(&deps)
	StubBuild(&deps, []build.Function{
		{Route: "api", Framework: appbuild.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	return root, journal, deps
}

func SetLoggedIn(deps *cmddeps.Deps) {
	deps.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
	}
}
