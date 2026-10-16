package aws

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

func writeBuild(t *testing.T, root, app, hosting string, imageConfig bool) {
	t.Helper()
	dir := buildoutput.AppRoot(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if hosting != "" {
		if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), []byte(hosting), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if imageConfig {
		if err := os.WriteFile(filepath.Join(dir, naming.ImageConfigFile), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestADeployNeedsThePartsItsBuildsUse(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuild(t, root, "web", `{"version":1,"framework":"next","needs":{"revalidation":{}}}`, true)
	writeBuild(t, root, "docs", `{"version":1,"framework":"next"}`, false)
	needed, err := partsNeededBy(root, []builtApp{{name: "web", framework: "next"}, {name: "docs", framework: "next"}})
	if err != nil {
		t.Fatalf("partsNeededBy() = %v", err)
	}

	want := []string{imageoptimizer.Part, taginvalidator.Part}
	if !slices.Equal(needed, want) {
		t.Errorf("partsNeededBy() = %v, want %v: web optimizes images and revalidates", needed, want)
	}
}

func TestADeployWhoseBuildsUseNeitherNeedsNoPart(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeBuild(t, root, "api", `{"version":1,"framework":"go"}`, false)

	needed, err := partsNeededBy(root, []builtApp{{name: "api", framework: "go"}})
	if err != nil {
		t.Fatalf("partsNeededBy() = %v", err)
	}
	if len(needed) != 0 {
		t.Errorf("partsNeededBy() = %v, want none", needed)
	}
}

func TestAnAppWithNoBuildToReadIsPresumedToUseWhatItsFrameworkCan(t *testing.T) {
	t.Parallel()

	needed, err := partsNeededBy(t.TempDir(), []builtApp{{name: "web", framework: "next"}})
	if err != nil {
		t.Fatalf("partsNeededBy() = %v", err)
	}
	if want := []string{imageoptimizer.Part, taginvalidator.Part}; !slices.Equal(needed, want) {
		t.Errorf("partsNeededBy() = %v, want %v, as the app's serving presumes for a next app it cannot read the hosting of", needed, want)
	}
}
