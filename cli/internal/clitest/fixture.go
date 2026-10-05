package clitest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func DiscoveryDir(root string) string {
	return filepath.Join(root, discovery.DefaultRootDirName)
}

func AttachTerminalSink(invocation commands.Invocation, w io.Writer) {
	invocation.Events.Attach(terminal.NewSink(invocation.Presentation(w), w))
}

func NewInvocation() commands.Invocation {
	return commands.Invocation{
		Events:          run.NewBus(time.Now),
		Presentation:    func(io.Writer) terminal.Presentation { return terminal.Resolve(terminal.Conditions{}) },
		StdinIsTerminal: func(io.Reader) bool { return false },
		ConfigPath:      func() string { return os.Getenv(commands.ConfigEnvVar) },
	}
}

func ResolveJSONPresentation(io.Writer) terminal.Presentation {
	return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
}

func WritePrebuiltFunction(t *testing.T, root, app, route string) {
	t.Helper()
	dir := filepath.Join(root, statedir.Name, "output", "apps", app, "functions", route+".func")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"framework": map[string]string{"name": "node"},
		"entryFile": "index.handler",
		"app":       app,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0o644); err != nil {
		t.Fatal(err)
	}
}

func IsolateConfigHome() func() {
	dir, err := os.MkdirTemp("", "ocel-cli-test-config-")
	if err != nil {
		panic(err)
	}
	confighome.Set(dir, func(name, value string) { os.Setenv(name, value) })
	os.Unsetenv(commands.ConfigEnvVar)
	return func() { os.RemoveAll(dir) }
}

func UnsetColorEnv() {
	for _, name := range []string{"FORCE_COLOR", "CLICOLOR_FORCE", "GITHUB_ACTIONS"} {
		os.Unsetenv(name)
	}
}

func UnsetGitEnv() {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		os.Unsetenv(name)
	}
}

func writeProject(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider and POSIX symlinks")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}

	t.Setenv(providerprocess.ReadyTimeoutEnvVar, "5s")

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
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+processenv.DevServerEnvVar+`), {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+processenv.DevServerTokenEnvVar+` },
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

func InstallProvider(t *testing.T, name string, place func(executable string) error) string {
	t.Helper()

	dir := os.Getenv(executables.OverrideEnvVar)
	if dir == "" {
		dir = t.TempDir()
		t.Setenv(executables.OverrideEnvVar, dir)
	}
	platformDir := filepath.Join(dir, string(executables.KindProvider), name, version.Version, runtime.GOOS+"-"+runtime.GOARCH)
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", platformDir, err)
	}
	executable := filepath.Join(platformDir, executables.ExecutableName(executables.KindProvider, name, runtime.GOOS))
	if err := place(executable); err != nil {
		t.Fatalf("install the %s provider at %s: %v", name, executable, err)
	}
	return executable
}

const FixtureSlug = "test-app"

func AddFakeProviderIDs() {
	configdoc.AddKnownIDs(string(fake.Vendor), []string{string(fake.KindRelay), string(fake.KindDirect)}, []string{string(fake.KindZone)})
}

func FixtureImage(app string) string {
	sum := sha256.Sum256([]byte("ocel-test-image/" + app))
	return "ocel/" + FixtureSlug + "/" + app + "@sha256:" + hex.EncodeToString(sum[:])
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
    fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+processenv.DevServerEnvVar+`), {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+processenv.DevServerTokenEnvVar+` },
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

func LoadLoggedInCredentials() (console.Credentials, error) {
	return console.Credentials{APIURL: "https://api.example.com", AccessToken: "tok"}, nil
}
