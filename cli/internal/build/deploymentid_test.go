package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/constants"
)

func TestAFreshBuildSupersedesTheDeploymentIDTheLastOneRecorded(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeBuildScript(t, root)
	cfg := &project.Project{Dir: root, Apps: []project.App{nextApp("web", "apps/web")}}
	builder := nodeOnly{node: func(context.Context, string, []byte, Log) error { return nil }}

	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	first, err := DeploymentID(root, "web")
	if err != nil {
		t.Fatalf("DeploymentID: %v", err)
	}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := DeploymentID(root, "web")
	if err != nil {
		t.Fatalf("DeploymentID: %v", err)
	}
	if first == second {
		t.Errorf("both builds recorded %q, want each build its own id", first)
	}
}

func TestDeploymentID(t *testing.T) {
	t.Parallel()

	t.Run("an app with no id points at ocel build", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "web"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := writeDeploymentID(root, "web", "d1a2b3c4d5e6f708192a3b4c5d6e7f80"); err != nil {
			t.Fatal(err)
		}
		_, err := DeploymentID(root, "admin")
		if err == nil || !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("DeploymentID err = %v, want it to point at `ocel build`", err)
		}
		if err == nil || !strings.Contains(err.Error(), "admin") {
			t.Errorf("DeploymentID err = %v, want it to name the app", err)
		}
	})

	t.Run("an id that is not what a build mints points at ocel build", func(t *testing.T) {
		t.Parallel()

		for _, recorded := range []string{
			"",
			"   ",
			"dep1",
			"D1A2B3C4D5E6F708192A3B4C5D6E7F80",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f8",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f800",
			"../../etc/passwd",
			"d1a2b3c4d5e6f708192a3b4c5d6e7f80 extra",
		} {
			root := t.TempDir()
			if err := writeDeploymentID(root, "web", recorded); err != nil {
				t.Fatal(err)
			}
			_, err := DeploymentID(root, "web")
			if err == nil || !strings.Contains(err.Error(), "ocel build") {
				t.Errorf("DeploymentID with %q recorded: err = %v, want it to point at `ocel build`", recorded, err)
			}
		}
	})

	t.Run("reads back the id a build minted", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		minted, err := mintDeploymentID()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeDeploymentID(root, "web", minted); err != nil {
			t.Fatal(err)
		}
		got, err := DeploymentID(root, "web")
		if err != nil {
			t.Fatalf("DeploymentID: %v", err)
		}
		if got != minted {
			t.Errorf("DeploymentID = %q, want %q", got, minted)
		}
	})
}
