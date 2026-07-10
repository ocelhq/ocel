package deploycollector

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestCollect_FixtureProject_CollectsEveryDeclareWithTypedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX-style fixture entrypoint")
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];

function declareResource(body: unknown) {
  globalThis.__ocelRegister.push(
    fetch(new URL("/resources.v1.ResourceService/Declare", process.env.OCEL_DEV_SERVER), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
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
		ProjectID: "proj_123",
		Dir:       root,
		Discovery: projectconfig.Discovery{Paths: []string{"ocel"}},
	}

	var stdout, stderr bytes.Buffer
	resources, err := Collect(context.Background(), cfg, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Collect: %v; stderr=%s", err, stderr.String())
	}

	if len(resources) != 2 {
		t.Fatalf("Collect() returned %d resources, want 2: %+v", len(resources), resources)
	}

	byName := make(map[string]string, len(resources))
	for _, r := range resources {
		if r.Type != resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES {
			t.Errorf("resource %q Type = %v, want RESOURCE_TYPE_POSTGRES", r.Name, r.Type)
		}
		byName[r.Name] = r.Postgres.GetVersion()
	}

	want := map[string]string{"main": "17", "reporting": "16"}
	for name, wantVersion := range want {
		if got := byName[name]; got != wantVersion {
			t.Errorf("resource %q Postgres.Version = %q, want %q", name, got, wantVersion)
		}
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
