package fake_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const fakePath = "github.com/ocelhq/ocel/pkg/provider/fake"

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

func importsOf(t *testing.T, source string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s for its imports: %v", source, err)
		return nil
	}
	imports := make([]string, 0, len(file.Imports))
	for _, imported := range file.Imports {
		quoted, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Errorf("read an import path in %s: %v", source, err)
			continue
		}
		imports = append(imports, quoted)
	}
	return imports
}

func reachesTheFake(imports []string) bool {
	return slices.ContainsFunc(imports, func(imported string) bool {
		return imported == fakePath || strings.HasPrefix(imported, fakePath+"/")
	})
}

func isTestHelperDir(dir string) bool {
	return strings.HasSuffix(filepath.Base(dir), "test")
}

func importPathOf(t *testing.T, dir string) string {
	t.Helper()

	for at := dir; ; at = filepath.Dir(at) {
		gomod, err := os.ReadFile(filepath.Join(at, "go.mod"))
		if err == nil {
			rel, err := filepath.Rel(at, dir)
			if err != nil {
				t.Fatal(err)
			}
			return path.Join(modulePath(t, gomod), filepath.ToSlash(rel))
		}
		if filepath.Dir(at) == at {
			t.Fatalf("no go.mod above %s", dir)
		}
	}
}

func modulePath(t *testing.T, gomod []byte) string {
	t.Helper()

	for _, line := range strings.Split(string(gomod), "\n") {
		if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(module)
		}
	}
	t.Fatal("a go.mod names no module")
	return ""
}

func TestTheFakeIsImportedByTestCodeAlone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	referenceBinary := filepath.Join(root, "pkg", "provider", "fake", "cmd") + string(filepath.Separator)
	var offenders []string
	scanned, witnesses := 0, 0
	helpers := map[string]bool{}
	productionImports := map[string][]string{}

	if err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
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
		if !strings.HasSuffix(file, ".go") {
			return nil
		}
		imports := importsOf(t, file)
		if strings.HasSuffix(file, "_test.go") {
			if reachesTheFake(imports) {
				witnesses++
			}
			return nil
		}
		if strings.HasPrefix(file, referenceBinary) {
			return nil
		}
		scanned++
		rel, err := filepath.Rel(root, file)
		if err != nil {
			rel = file
		}
		rel = filepath.ToSlash(rel)
		if isTestHelperDir(filepath.Dir(file)) && reachesTheFake(imports) {
			helpers[importPathOf(t, filepath.Dir(file))] = true
			return nil
		}
		productionImports[rel] = imports
		if reachesTheFake(imports) {
			offenders = append(offenders, rel)
		}
		return nil
	}); err != nil {
		t.Fatalf("sweep the tree for imports of the fake: %v", err)
	}
	for rel, imports := range productionImports {
		if slices.ContainsFunc(imports, func(imported string) bool { return helpers[imported] }) {
			offenders = append(offenders, rel)
		}
	}
	slices.Sort(offenders)

	if scanned == 0 {
		t.Fatal("the sweep read no production Go file, so it checks nothing against the criterion it claims to prove")
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
