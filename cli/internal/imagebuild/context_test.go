package imagebuild

import (
	"context"
	gofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func laidOut(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mounted(t *testing.T, root string) []string {
	t.Helper()
	fs, err := contextFS(root)
	if err != nil {
		t.Fatalf("contextFS(%s) = %v", root, err)
	}
	var seen []string
	if err := fs.Walk(context.Background(), "", func(path string, entry gofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		seen = append(seen, filepath.ToSlash(path))
		return nil
	}); err != nil {
		t.Fatalf("walking the build context = %v", err)
	}
	slices.Sort(seen)
	return seen
}

func TestTheContextCarriesNoInstalledDependenciesNoHistoryAndNoEarlierBuild(t *testing.T) {
	root := laidOut(t, map[string]string{
		"package.json":                         `{"name":"root"}`,
		"apps/web/server.js":                   "listen()\n",
		"node_modules/express/index.js":        "module.exports = {}\n",
		"apps/web/node_modules/lib/index.js":   "module.exports = {}\n",
		".git/HEAD":                            "ref: refs/heads/main\n",
		".ocel/output/functions/exp.func/x.js": "stale\n",
		"apps/web/.ocel/dist/x.js":             "stale\n",
	})

	carried := mounted(t, root)

	if want := []string{"apps/web/server.js", "package.json"}; !slices.Equal(carried, want) {
		t.Errorf("the build context carries %v, want %v — installed dependencies, git history and an earlier build's output are none of the app's source", carried, want)
	}
}

func TestTheContextHonoursTheDockerignoreBesideIt(t *testing.T) {
	root := laidOut(t, map[string]string{
		".dockerignore":     "*.log\nsecrets/\n",
		"package.json":      `{"name":"root"}`,
		"server.js":         "listen()\n",
		"debug.log":         "noisy\n",
		"secrets/token.txt": "shhh\n",
	})

	carried := mounted(t, root)

	if want := []string{".dockerignore", "package.json", "server.js"}; !slices.Equal(carried, want) {
		t.Errorf("the build context carries %v, want %v — what a %s excludes never reaches the daemon", carried, want, DockerignoreName)
	}
}
