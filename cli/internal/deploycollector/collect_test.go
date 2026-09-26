package deploycollector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/sdkversion"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestCollect(t *testing.T) {
	t.Run("a project with no discovery directory declares nothing and does not fail", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX-style fixture entrypoint")
		}

		cfg := &projectconfig.Config{
			Slug: "test-app",
			Dir:  t.TempDir(),
		}

		var stdout, stderr bytes.Buffer
		resources, err := PrepareAndCollect(context.Background(), cfg, envgate.New(emptyValues{}, envgate.Scope{}), &stdout, &stderr)
		if err != nil {
			t.Fatalf("Collect: %v; stderr=%s", err, stderr.String())
		}
		if len(resources) != 0 {
			t.Fatalf("Collect() returned %d resources, want none: %+v", len(resources), resources)
		}
	})

	t.Run("a fixture project yields every declare with its typed config", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX-style fixture entrypoint")
		}

		root := t.TempDir()
		writeFile(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

function declareResource(body: unknown) {
  globalThis.__ocelRegister.push(
    fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+` },
      body: JSON.stringify(body),
    }),
  );
}

declareResource({
  resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
  postgres: { version: "17" },
});
declareResource({
  resource: { type: "RESOURCE_TYPE_POSTGRES", name: "reporting" },
  postgres: { version: "16" },
});
export {};
`)

		cfg := &projectconfig.Config{
			Slug:      "test-app",
			Dir:       root,
			Discovery: projectconfig.Discovery{Paths: []string{constants.DefaultDiscoveryDirName}},
		}

		var stdout, stderr bytes.Buffer
		resources, err := PrepareAndCollect(context.Background(), cfg, envgate.New(emptyValues{}, envgate.Scope{}), &stdout, &stderr)
		if err != nil {
			t.Fatalf("Collect: %v; stderr=%s", err, stderr.String())
		}

		if len(resources) != 2 {
			t.Fatalf("Collect() returned %d resources, want 2: %+v", len(resources), resources)
		}

		byName := make(map[string]string, len(resources))
		for _, r := range resources {
			if r.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
				t.Errorf("resource %q Type = %v, want %v", r.Name, r.Type, resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES)
			}
			byName[r.Name] = r.Postgres.GetVersion()
		}

		want := map[string]string{"main": "17", "reporting": "16"}
		for name, wantVersion := range want {
			if got := byName[name]; got != wantVersion {
				t.Errorf("resource %q Postgres.Version = %q, want %q", name, got, wantVersion)
			}
		}
	})
}

func TestACallToTheCollectorThatIsNotTheChildsIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX-style fixture entrypoint")
	}

	root := t.TempDir()
	statuses := filepath.Join(t.TempDir(), "statuses.json")
	t.Setenv("OCEL_TEST_STATUS_FILE", statuses)
	writeFile(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

const body = JSON.stringify({
  resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
  postgres: { version: "17" },
});

const declare = (headers: Record<string, string>) =>
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body,
  });

globalThis.__ocelRegister.push(
  (async () => {
    const token = "Bearer " + process.env.`+constants.DevServerTokenEnvName+`;
    const statuses = [
      (await declare({})).status,
      (await declare({ Authorization: "Bearer guessed" })).status,
      (await declare({ Authorization: token, Origin: "http://evil.example" })).status,
      (await declare({ Authorization: token })).status,
    ];
    await (await import("node:fs/promises")).writeFile(
      process.env.OCEL_TEST_STATUS_FILE!,
      JSON.stringify(statuses),
    );
  })(),
);
export {};
`)

	cfg := &projectconfig.Config{
		Slug:      "test-app",
		Dir:       root,
		Discovery: projectconfig.Discovery{Paths: []string{constants.DefaultDiscoveryDirName}},
	}

	var stdout, stderr bytes.Buffer
	resources, err := PrepareAndCollect(context.Background(), cfg, envgate.New(emptyValues{}, envgate.Scope{}), &stdout, &stderr)
	if err != nil {
		t.Fatalf("Collect: %v; stderr=%s", err, stderr.String())
	}

	raw, err := os.ReadFile(statuses)
	if err != nil {
		t.Fatalf("read the statuses the child recorded: %v", err)
	}
	var got []int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	if want := []int{403, 403, 403, 200}; !slices.Equal(got, want) {
		t.Errorf("statuses = %v, want %v: only the child with the token, from the collector's own origin, may declare", got, want)
	}
	if len(resources) != 1 {
		t.Fatalf("Collect() returned %d resources, want only the one declare that sent the token: %+v", len(resources), resources)
	}
}

func TestCollectRunsTheBundlePrepareAlreadyBuilt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX-style fixture entrypoint")
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), "export {};\n")

	cfg := &projectconfig.Config{Slug: "test-app", Dir: root}
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if prepared.Fingerprint() == "" {
		t.Fatal("Fingerprint() is empty, want the hash of the bundle Prepare built")
	}

	writeFile(t, filepath.Join(root, constants.ProjectStateDirName, "entry.mjs"), `
await fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+` },
  body: JSON.stringify({
    resource: { type: "RESOURCE_TYPE_POSTGRES", name: "prepared-once" },
    postgres: { version: "17" },
  }),
});
`)

	var stdout, stderr bytes.Buffer
	resources, err := Collect(context.Background(), cfg, envgate.New(emptyValues{}, envgate.Scope{}), prepared, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Collect: %v; stderr=%s", err, stderr.String())
	}
	if len(resources) != 1 || resources[0].Name != "prepared-once" {
		t.Fatalf("Collect() returned %+v, want the declare of the bundle Prepare built rather than a second bundle", resources)
	}
}

func TestAnSDKOfAnotherReleaseIsRefusedWithTheUpgradeThatFixesIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX-style fixture entrypoint")
	}
	wanted := version.Version
	version.Version = "0.0.3"
	t.Cleanup(func() { version.Version = wanted })

	root := t.TempDir()
	writeFile(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + process.env.`+constants.DevServerTokenEnvName+`,
      "`+constants.SDKVersionHeader+`": "js/0.0.2",
    },
    body: JSON.stringify({
      resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" },
      postgres: { version: "17" },
    }),
  }),
);
export {};
`)
	cfg := &projectconfig.Config{
		Slug:      "test-app",
		Dir:       root,
		Discovery: projectconfig.Discovery{Paths: []string{constants.DefaultDiscoveryDirName}},
	}

	var stdout, stderr bytes.Buffer
	resources, err := PrepareAndCollect(context.Background(), cfg, envgate.New(emptyValues{}, envgate.Scope{}), &stdout, &stderr)
	var mismatch *sdkversion.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Collect() = %+v, %v; want the SDK refused for its version; stderr=%s", resources, err, stderr.String())
	}
	want := "the JavaScript SDK (ocel) is version 0.0.2 and this CLI is version 0.0.3; an SDK works with the CLI of its own release — run `npm i ocel@0.0.3`"
	if err.Error() != want {
		t.Fatalf("Collect() err = %q, want %q", err, want)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type emptyValues struct{}

func (emptyValues) List(context.Context) ([]envgate.Stored, error) { return nil, nil }

func (emptyValues) Reveal(context.Context, []envgate.Address) (map[envgate.Cell]string, error) {
	return nil, nil
}
