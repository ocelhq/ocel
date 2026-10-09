package buildoutput_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
)

func TestTheLayoutIsTheOneTheJSAdaptersWrite(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "frameworks", "node", "build", "fixtures", "build-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	var adapters map[string]any
	if err := json.Unmarshal(raw, &adapters); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"hostingFile":        buildoutput.HostingFile,
		"hostingVersion":     float64(buildoutput.HostingVersion),
		"functionConfigFile": buildoutput.FunctionConfigFile,
		"functionsDir":       buildoutput.FunctionsDir,
		"functionDirSuffix":  buildoutput.FunctionDirSuffix,
		"rootFunction":       buildoutput.RootFunction,
		"rootFunctionDir":    buildoutput.RootFunctionDir,
		"staticDir":          edge.StaticAssetDir,
	}
	if !reflect.DeepEqual(adapters, want) {
		t.Errorf("the JS adapters write the layout %v, and the CLI reads %v", adapters, want)
	}
}

func TestTheOutputRootOfAProjectDirectoryThatIsNotAbsoluteIsRefused(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{"", ".", "project"} {
		if root, err := buildoutput.Root(dir); err == nil {
			t.Errorf("Root(%q) = %q, want an error: a relative root resolves against whatever directory the process runs in", dir, root)
		}
	}
}

func TestTheOutputRootSitsInsideTheProjectDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := buildoutput.Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, filepath.FromSlash(buildoutput.Dir)); root != want {
		t.Errorf("Root(%q) = %q, want %q", dir, root, want)
	}
}
