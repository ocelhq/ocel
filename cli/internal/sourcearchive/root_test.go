package sourcearchive_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
)

func TestRootIsTheOutermostOfTheGitRootAndTheWorkspacesHoldingTheProject(t *testing.T) {
	for _, tt := range []struct {
		name    string
		files   map[string]string
		dirs    []string
		project string
		want    string
	}{
		{
			name:    "the project directory when nothing encloses it",
			files:   map[string]string{"app/package.json": "{}"},
			project: "app",
			want:    "app",
		},
		{
			name:    "the git root above the project",
			files:   map[string]string{"repo/apps/web/package.json": "{}"},
			dirs:    []string{"repo/.git"},
			project: "repo/apps/web",
			want:    "repo",
		},
		{
			name:    "a git worktree's .git file marks the root",
			files:   map[string]string{"repo/.git": "gitdir: /elsewhere", "repo/web/package.json": "{}"},
			project: "repo/web",
			want:    "repo",
		},
		{
			name: "the JS workspace that lists the project",
			files: map[string]string{
				"mono/pnpm-workspace.yaml":   "packages:\n  - apps/*\n",
				"mono/apps/web/package.json": "{}",
			},
			project: "mono/apps/web",
			want:    "mono",
		},
		{
			name: "a JS workspace that does not list the project",
			files: map[string]string{
				"mono/package.json":          `{"workspaces": ["packages/*"]}`,
				"mono/apps/web/package.json": "{}",
			},
			project: "mono/apps/web",
			want:    "mono/apps/web",
		},
		{
			name: "the cargo workspace above the project",
			files: map[string]string{
				"rs/Cargo.toml":     "[workspace]\nmembers = [\"api\"]\n",
				"rs/api/Cargo.toml": "[package]\nname = \"api\"\n",
			},
			project: "rs/api",
			want:    "rs",
		},
		{
			name: "a crate that is no workspace",
			files: map[string]string{
				"rs/Cargo.toml":     "[package]\nname = \"top\"\n",
				"rs/api/Cargo.toml": "[package]\nname = \"api\"\n",
			},
			project: "rs/api",
			want:    "rs/api",
		},
		{
			name: "the git root above a workspace",
			files: map[string]string{
				"repo/js/pnpm-workspace.yaml": "packages:\n  - web\n",
				"repo/js/web/package.json":    "{}",
			},
			dirs:    []string{"repo/.git"},
			project: "repo/js/web",
			want:    "repo",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			write(t, base, tt.files)
			for _, dir := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(dir)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			got, err := sourcearchive.Root(filepath.Join(base, filepath.FromSlash(tt.project)))
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(base, filepath.FromSlash(tt.want)); got != want {
				t.Fatalf("root = %s, want %s", got, want)
			}
		})
	}
}

func TestPackUploadsFromTheRootAboveTheProject(t *testing.T) {
	base := t.TempDir()
	write(t, base, map[string]string{"apps/web/ocel.json": "{}", "packages/ui/button.tsx": "x"})
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := sourcearchive.Pack(context.Background(), filepath.Join(base, "apps", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if archive.Root != base {
		t.Fatalf("root = %s, want %s", archive.Root, base)
	}
	if got := paths(archive); len(got) != 2 || got[0] != "apps/web/ocel.json" || got[1] != "packages/ui/button.tsx" {
		t.Fatalf("uploaded %q", got)
	}
}
