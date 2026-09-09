package doctor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func goProject(t *testing.T, configName string) *projectconfig.Config {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"go.mod", configName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &projectconfig.Config{Slug: "fixture", Dir: dir, Path: filepath.Join(dir, configName)}
}

func TestAGoProjectNeedsNoNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	if reasons := nodeReasons(cfg); len(reasons) != 0 {
		t.Fatalf("nodeReasons() = %v, want none", reasons)
	}
	if _, held := nodeCheck(context.Background(), cfg); held {
		t.Fatal("a Go project was checked for node")
	}
}

func TestATypeScriptConfigNeedsNodeAndSaysSo(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.config.ts")
	reasons := nodeReasons(cfg)
	if !slices.Contains(reasons, "ocel.config.ts is TypeScript") {
		t.Fatalf("nodeReasons() = %v, want the TypeScript config named", reasons)
	}

	got, held := nodeCheck(context.Background(), cfg)
	if !held {
		t.Fatal("a TypeScript config was not checked for node")
	}
	if !strings.Contains(got.text, "ocel.config.ts is TypeScript") {
		t.Fatalf("check text = %q, want it to name why node is needed", got.text)
	}
}

func TestJavaScriptInTheProjectNeedsNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	if err := os.WriteFile(filepath.Join(cfg.Dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if reasons := nodeReasons(cfg); !slices.Contains(reasons, "this project holds JavaScript") {
		t.Fatalf("nodeReasons() = %v, want the JavaScript named", reasons)
	}
}

func TestConfiguredTransformsNeedNode(t *testing.T) {
	t.Parallel()

	cfg := goProject(t, "ocel.json")
	cfg.Provider = &projectconfig.ProviderDescriptor{Name: "aws", Options: json.RawMessage(`{"transforms":["./transforms/default.transform.ts"]}`)}
	if reasons := nodeReasons(cfg); !slices.Contains(reasons, "the provider is configured with transforms") {
		t.Fatalf("nodeReasons() = %v, want the transforms named", reasons)
	}

	cfg.Provider.Options = json.RawMessage(`{"transforms":[]}`)
	if reasons := nodeReasons(cfg); len(reasons) != 0 {
		t.Fatalf("nodeReasons() = %v, want none for an empty transform list", reasons)
	}
}

func TestNodeMissingFromPATHFailsOnlyWhereItIsNeeded(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	if _, held := nodeCheck(context.Background(), goProject(t, "ocel.json")); held {
		t.Fatal("a Go project failed over node it never runs")
	}

	got, held := nodeCheck(context.Background(), goProject(t, "ocel.config.ts"))
	if !held || got.verdict != verdictFail {
		t.Fatalf("nodeCheck() = %+v, %v, want a failure naming the missing node", got, held)
	}
	if !strings.Contains(got.text, "not on PATH") {
		t.Fatalf("check text = %q, want it to say node is not on PATH", got.text)
	}
}
