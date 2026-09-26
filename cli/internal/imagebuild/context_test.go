package imagebuild

import (
	"context"
	gofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
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

func TestTheContextIncludesNoInstalledDependenciesNoHistoryAndNoEarlierBuild(t *testing.T) {
	root := laidOut(t, map[string]string{
		"package.json":                       `{"name":"root"}`,
		"apps/web/server.js":                 "listen()\n",
		"node_modules/express/index.js":      "module.exports = {}\n",
		"apps/web/node_modules/lib/index.js": "module.exports = {}\n",
		".git/HEAD":                          "ref: refs/heads/main\n",
		constants.ProjectStateDirName + "/output/functions/exp.func/x.js": "stale\n",
		"apps/web/" + constants.ProjectStateDirName + "/dist/x.js":        "stale\n",
	})

	files := mounted(t, root)

	if want := []string{"apps/web/server.js", "package.json"}; !slices.Equal(files, want) {
		t.Errorf("the build context contains %v, want %v — installed dependencies, git history and an earlier build's output are none of the app's source", files, want)
	}
}

func TestThePlanAndTheContextAgreeOnWhatTheDaemonWillReceive(t *testing.T) {
	root := laidOut(t, map[string]string{
		".dockerignore":                 "*.log\n!keep.log\nsecrets\n",
		"package.json":                  `{"name":"root"}`,
		"server.js":                     "listen()\n",
		"debug.log":                     "noisy\n",
		"keep.log":                      "wanted\n",
		"secrets/token.txt":             "shhh\n",
		"node_modules/express/index.js": "module.exports = {}\n",
	})

	files := mounted(t, root)
	outside, err := outsideTheContext(root)
	if err != nil {
		t.Fatalf("outsideTheContext(%s) = %v", root, err)
	}

	for _, path := range []string{
		"package.json",
		"server.js",
		"debug.log",
		"keep.log",
		"secrets/token.txt",
		"node_modules/express/index.js",
	} {
		if walked := slices.Contains(files, path); walked == outside(path) {
			t.Errorf("%q is a path the context %s and the plan filter %s: a path the plan copies but the context does not include fails the build at the daemon, and one the plan drops but the context includes is lost from the image",
				path, said(walked), said(!outside(path)))
		}
	}
}

func said(included bool) string {
	if included {
		return "includes"
	}
	return "leaves out"
}

func TestTheContextHonoursTheDockerignoreBesideIt(t *testing.T) {
	root := laidOut(t, map[string]string{
		".dockerignore":     "*.log\nsecrets/\n",
		"package.json":      `{"name":"root"}`,
		"server.js":         "listen()\n",
		"debug.log":         "noisy\n",
		"secrets/token.txt": "shhh\n",
	})

	files := mounted(t, root)

	if want := []string{".dockerignore", "package.json", "server.js"}; !slices.Equal(files, want) {
		t.Errorf("the build context contains %v, want %v — what a %s excludes never reaches the daemon", files, want, DockerignoreName)
	}
}
