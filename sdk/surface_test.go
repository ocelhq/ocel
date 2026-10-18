package ocel_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func internalImports(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.Contains(imported, "/internal/") {
			continue
		}
		name := path.Base(imported)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = imported
	}
	return names
}

func internalTypesIn(expr ast.Node, internal map[string]string) []string {
	var found []string
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.StructType:
			for _, field := range n.Fields.List {
				for _, name := range field.Names {
					if name.IsExported() {
						found = append(found, internalTypesIn(field.Type, internal)...)
					}
				}
			}
			return false
		case *ast.InterfaceType:
			for _, method := range n.Methods.List {
				for _, name := range method.Names {
					if name.IsExported() {
						found = append(found, internalTypesIn(method.Type, internal)...)
					}
				}
			}
			return false
		case *ast.SelectorExpr:
			if pkg, ok := n.X.(*ast.Ident); ok {
				if imported, ok := internal[pkg.Name]; ok {
					found = append(found, imported+"."+n.Sel.Name)
				}
			}
		}
		return true
	})
	return found
}

func TestTheExportedSurfaceNamesNoInternalType(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	dirs := []string{"."}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "internal" && !strings.HasPrefix(entry.Name(), ".") {
			dirs = append(dirs, entry.Name())
		}
	}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			internal := internalImports(file)
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					for _, leak := range internalTypesIn(d.Type, internal) {
						t.Errorf("%s: %s names %s", name, d.Name.Name, leak)
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok || !typeSpec.Name.IsExported() {
							continue
						}
						for _, leak := range internalTypesIn(typeSpec.Type, internal) {
							t.Errorf("%s: %s names %s", name, typeSpec.Name.Name, leak)
						}
					}
				}
			}
		}
	}
}
