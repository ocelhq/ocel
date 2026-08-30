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
			t.Skip("no go.work above this package, so there is no tree to sweep")
		}
		dir = parent
	}
}

func TestTheFakeIsImportedByTestCodeAlone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	ownTree := filepath.Join(root, "pkg", "providerkit", "fake")
	var offenders []string

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
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, ownTree+string(filepath.Separator)) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return nil
		}
		for _, imported := range file.Imports {
			quoted, err := strconv.Unquote(imported.Path.Value)
			if err != nil || quoted != fakePath {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		t.Fatalf("sweep the tree for imports of the fake: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("the in-memory fake is imported by production code in %s: "+
			"a test double on a production path accepts writes it loses, which is what wiring it into the vps provider cost",
			strings.Join(offenders, ", "))
	}
}
