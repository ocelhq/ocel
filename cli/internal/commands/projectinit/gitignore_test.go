package projectinit

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func gitRepository(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func initIn(t *testing.T, dir string) {
	t.Helper()
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if _, err := runInit(context.Background(), dependencies, dir, "my-app", initOptions{provider: "fake"}); err != nil {
		t.Fatalf("runInit err = %v; stdout=%s", err, stdout.String())
	}
}

func readGitignore(t *testing.T, dir string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	return string(data), true
}

func TestInitIgnoresTheStateDirectory(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		repository bool
		existing   string
		exists     bool
		want       string
		wantExists bool
	}{
		{
			name:       "a git repository with no .gitignore gets one that ignores .ocel/",
			repository: true,
			want:       statedir.Name + "/\n",
			wantExists: true,
		},
		{
			name:       "an existing .gitignore keeps its content and gains .ocel/ on a line of its own",
			repository: true,
			existing:   "node_modules\n# build\ndist",
			exists:     true,
			want:       "node_modules\n# build\ndist\n" + statedir.Name + "/\n",
			wantExists: true,
		},
		{
			name:       "an existing .gitignore ending in a newline gains .ocel/ after it",
			repository: true,
			existing:   "node_modules\n",
			exists:     true,
			want:       "node_modules\n" + statedir.Name + "/\n",
			wantExists: true,
		},
		{
			name:       "a .gitignore that already ignores .ocel is left alone",
			repository: true,
			existing:   "node_modules\n" + statedir.Name + "\n",
			exists:     true,
			want:       "node_modules\n" + statedir.Name + "\n",
			wantExists: true,
		},
		{
			name:       "a .gitignore outside a git repository still gains .ocel/",
			existing:   "node_modules\n",
			exists:     true,
			want:       "node_modules\n" + statedir.Name + "/\n",
			wantExists: true,
		},
		{
			name:       "a .gitignore outside a git repository that already lists it anchored at its root is left alone",
			existing:   "/" + statedir.Name + "/\n",
			exists:     true,
			want:       "/" + statedir.Name + "/\n",
			wantExists: true,
		},
		{
			name:       "a .gitignore that lists the state directory indented ignores nothing, so it gains a line of its own",
			repository: true,
			existing:   "  " + statedir.Name + "/\n",
			exists:     true,
			want:       "  " + statedir.Name + "/\n" + statedir.Name + "/\n",
			wantExists: true,
		},
		{
			name:       "a .gitignore outside a git repository that lists it with trailing spaces is left alone",
			existing:   "/" + statedir.Name + "/  \r\n",
			exists:     true,
			want:       "/" + statedir.Name + "/  \r\n",
			wantExists: true,
		},
		{
			name: "a directory outside a git repository with no .gitignore gets none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := initTestDir(t, "proj")
			if tc.repository {
				gitRepository(t, dir)
			}
			if tc.exists {
				if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(tc.existing), 0o644); err != nil {
					t.Fatalf("write .gitignore: %v", err)
				}
			}

			initIn(t, dir)

			got, exists := readGitignore(t, dir)
			if exists != tc.wantExists {
				t.Fatalf(".gitignore exists = %v after init, want %v (content %q)", exists, tc.wantExists, got)
			}
			if got != tc.want {
				t.Errorf(".gitignore = %q after init, want %q", got, tc.want)
			}
		})
	}
}

func TestInitLeavesTheGitignoreAloneWhenTheRepositoryAlreadyIgnoresTheStateDirectory(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{statedir.Name, statedir.Name + "/", "/apps/web/" + statedir.Name + "/"} {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()

			repository := t.TempDir()
			gitRepository(t, repository)
			if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(pattern+"\n"), 0o644); err != nil {
				t.Fatalf("write .gitignore: %v", err)
			}
			dir := filepath.Join(repository, "apps", "web")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("create project dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write package.json: %v", err)
			}

			initIn(t, dir)

			if got, exists := readGitignore(t, dir); exists {
				t.Errorf("init wrote %s/.gitignore = %q, want none: the repository's own .gitignore already ignores .ocel", dir, got)
			}
		})
	}
}
