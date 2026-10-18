package providerprocess

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestLaunchingAProviderWritesNothingIntoTheProject(t *testing.T) {
	dir := t.TempDir()
	_, _ = newLaunchSpec(context.Background(), &project.Project{Dir: dir, Path: filepath.Join(dir, project.DefaultFileName)}, executables.ChoosePinning(true))

	if _, err := os.Stat(filepath.Join(dir, buildoutput.StateDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat %s = %v, want nothing written: a provider never runs the node bundle", buildoutput.StateDir, err)
	}
}

func TestProviderConfigIncludesTheProjectTransformModules(t *testing.T) {
	modules := []string{"./transforms/network.transform.ts"}
	config, err := newProviderConfig(&project.Project{Transforms: modules}, &project.Provider{ID: "fake"})
	if err != nil {
		t.Fatalf("newProviderConfig: %v", err)
	}
	if !slices.Equal(config.GetTransforms(), modules) {
		t.Errorf("transforms = %v, want %v", config.GetTransforms(), modules)
	}
}

func TestProviderConfigNamesTheProjectDirectoryTheBuildIsUnder(t *testing.T) {
	config, err := newProviderConfig(&project.Project{Dir: "/work/shop"}, &project.Provider{ID: "fake"})
	if err != nil {
		t.Fatalf("newProviderConfig: %v", err)
	}
	if config.GetProjectDir() != "/work/shop" {
		t.Errorf("project dir = %q, want /work/shop: the provider reads the build from the project, not from whatever directory the CLI ran in", config.GetProjectDir())
	}
}

func TestProviderConfigIncludesTheProjectItConfigures(t *testing.T) {
	config, err := newProviderConfig(&project.Project{Slug: "shop"}, &project.Provider{ID: "fake"})
	if err != nil {
		t.Fatalf("newProviderConfig: %v", err)
	}
	if config.GetSlug() != "shop" {
		t.Errorf("slug = %q, want shop: a provider that records what it set on a shared machine names the project that set it", config.GetSlug())
	}
}

func TestProviderConfigIncludesTheDescriptorOptionsOpaquely(t *testing.T) {
	config, err := newProviderConfig(&project.Project{}, &project.Provider{
		ID:      "fake",
		Options: json.RawMessage(`{"size":"large"}`),
	})
	if err != nil {
		t.Fatalf("newProviderConfig: %v", err)
	}
	if got := config.GetOptions().GetFields()["size"].GetStringValue(); got != "large" {
		t.Errorf("size = %q, want large", got)
	}
}

func TestProviderConfigRefusesOptionsThatAreNotAJSONObject(t *testing.T) {
	_, err := newProviderConfig(&project.Project{}, &project.Provider{
		ID:      "fake",
		Options: json.RawMessage(`["large"]`),
	})
	if err == nil {
		t.Fatal("providerConfig err = nil, want a non-object options value refused")
	}
	if !strings.Contains(err.Error(), "are not an object") {
		t.Errorf("err = %v, want it to say the options are not an object", err)
	}
}

func TestProviderConfigLeavesAnUnconfiguredProviderWithoutOptions(t *testing.T) {
	config, err := newProviderConfig(&project.Project{}, &project.Provider{
		ID:      "fake",
		Options: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("newProviderConfig: %v", err)
	}
	if len(config.GetOptions().GetFields()) != 0 {
		t.Errorf("options = %v, want none for a descriptor declaring no options", config.GetOptions())
	}
}
