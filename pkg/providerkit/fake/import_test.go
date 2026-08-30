package fake_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const fakePath = "github.com/ocelhq/ocel/pkg/providerkit/fake"

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work above this package: the sweep has no tree to walk, and a sweep of nothing proves nothing")
		}
		dir = parent
	}
}

func reachesTheFake(t *testing.T, path string) bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s for its imports: %v", path, err)
		return false
	}
	for _, imported := range file.Imports {
		quoted, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Errorf("read an import path in %s: %v", path, err)
			continue
		}
		if quoted == fakePath || strings.HasPrefix(quoted, fakePath+"/") {
			return true
		}
	}
	return false
}

func TestTheFakeIsImportedByTestCodeAlone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	referenceBinary := filepath.Join(root, "pkg", "providerkit", "fake", "cmd") + string(filepath.Separator)
	var offenders []string
	scanned, witnesses := 0, 0

	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".claude", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			if reachesTheFake(t, path) {
				witnesses++
			}
			return nil
		}
		if strings.HasPrefix(path, referenceBinary) {
			return nil
		}
		scanned++
		if !reachesTheFake(t, path) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		offenders = append(offenders, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("sweep the tree for imports of the fake: %v", err)
	}

	if scanned == 0 {
		t.Fatal("the sweep read no production Go file, so it holds nothing to the criterion it claims to prove")
	}
	if witnesses == 0 {
		t.Fatalf("no test file imports %q, so the sweep is matching a path this tree no longer uses", fakePath)
	}
	if len(offenders) > 0 {
		t.Errorf("the in-memory fake is imported by production code in %s: "+
			"a test double on a production path accepts writes it loses, which is what wiring it into the vps provider cost",
			strings.Join(offenders, ", "))
	}
}
