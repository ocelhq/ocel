package clierror_test

import (
	"go/ast"
	"go/constant"
	"go/types"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"golang.org/x/tools/go/packages"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/docsurl"
)

const (
	errorPagesDir     = "../../../www/content/docs/errors"
	cliModuleDir      = "../../"
	clierrorPkgPath   = "github.com/ocelhq/ocel/cli/internal/clierror"
	errorTypeName     = "Error"
	codeFieldName     = "Code"
	codeConstantStart = "Code"
)

func TestEveryPublishedCodeHasADocsPageItsDocsURLPointsAt(t *testing.T) {
	for _, code := range clierror.Codes() {
		t.Run(code, func(t *testing.T) {
			page, err := url.Parse(docsurl.FormatErrorPage(code))
			if err != nil {
				t.Fatal(err)
			}
			if dir := path.Dir(page.Path); dir != "/docs/errors" {
				t.Fatalf("code %q has its docs page under %s, want /docs/errors", code, dir)
			}
			if _, err := os.Stat(filepath.Join(errorPagesDir, path.Base(page.Path)+".mdx")); err != nil {
				t.Fatalf("code %q has no docs page: %v", code, err)
			}
		})
	}
}

func TestEveryDocsPageNamesAPublishedCode(t *testing.T) {
	published := registeredCodes()
	entries, err := os.ReadDir(errorPagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".mdx" || name == "index.mdx" {
			continue
		}
		if code := strings.TrimSuffix(name, ".mdx"); !published[code] {
			t.Errorf("%s documents %q, which the CLI never raises", name, code)
		}
	}
}

func TestEveryRegisteredCodeMakesARunErrorTheProtoAccepts(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range clierror.Codes() {
		got := clierror.NewRunError(&clierror.Error{Code: code, Cause: os.ErrNotExist})
		if got.GetCode() != code {
			t.Errorf("code %q: run error code = %q, want the registered code", code, got.GetCode())
		}
		if err := validator.Validate(got); err != nil {
			t.Errorf("code %q: run error breaks the proto's rules: %v", code, err)
		}
	}
}

func TestEveryCodeConstantIsInTheRegistry(t *testing.T) {
	registered := registeredCodes()
	clierrorPkg := loadPackages(t, clierrorPkgPath)[0]
	scope := clierrorPkg.Types.Scope()
	for _, name := range scope.Names() {
		code, ok := scope.Lookup(name).(*types.Const)
		if !ok || !code.Exported() || !strings.HasPrefix(name, codeConstantStart) {
			continue
		}
		if value := constant.StringVal(code.Val()); !registered[value] {
			t.Errorf("clierror.%s (%q) is missing from clierror.Codes(), so it ships with no docs page", name, value)
		}
	}
}

func TestEveryCodedErrorTheCLIBuildsTakesItsCodeFromTheRegistry(t *testing.T) {
	registered := registeredCodes()
	codeIndex := codeFieldIndex(t)
	for _, pkg := range loadPackages(t, "./...") {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				for _, value := range codeValues(pkg.TypesInfo, node, codeIndex) {
					if !isRegisteredCode(pkg.TypesInfo, value, registered) {
						t.Errorf("%s: a clierror.Error takes its code from something other than a registered clierror.Code constant or another clierror.Error's code, so it has no docs page", pkg.Fset.Position(value.Pos()))
					}
				}
				return true
			})
		}
	}
}

func registeredCodes() map[string]bool {
	registered := map[string]bool{}
	for _, code := range clierror.Codes() {
		registered[code] = true
	}
	return registered
}

func loadPackages(t *testing.T, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  cliModuleDir,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) == 0 {
		t.Fatalf("loading %v failed", patterns)
	}
	return pkgs
}

func codeFieldIndex(t *testing.T) int {
	t.Helper()
	field, ok := reflect.TypeFor[clierror.Error]().FieldByName(codeFieldName)
	if !ok {
		t.Fatalf("clierror.Error has no %s field", codeFieldName)
	}
	return field.Index[0]
}

func codeValues(info *types.Info, node ast.Node, codeIndex int) []ast.Expr {
	switch node := node.(type) {
	case *ast.CompositeLit:
		if !isClierrorError(info.TypeOf(node)) {
			return nil
		}
		for i, element := range node.Elts {
			pair, keyed := element.(*ast.KeyValueExpr)
			if !keyed && i == codeIndex {
				return []ast.Expr{element}
			}
			if key, ok := pair.Key.(*ast.Ident); keyed && ok && key.Name == codeFieldName {
				return []ast.Expr{pair.Value}
			}
		}
	case *ast.AssignStmt:
		var values []ast.Expr
		for i, target := range node.Lhs {
			selector, ok := ast.Unparen(target).(*ast.SelectorExpr)
			if !ok || !isCodeField(info, selector) {
				continue
			}
			if len(node.Rhs) != len(node.Lhs) {
				values = append(values, node.Rhs[0])
				continue
			}
			values = append(values, node.Rhs[i])
		}
		return values
	}
	return nil
}

func isRegisteredCode(info *types.Info, value ast.Expr, registered map[string]bool) bool {
	var name *ast.Ident
	switch value := ast.Unparen(value).(type) {
	case *ast.Ident:
		name = value
	case *ast.SelectorExpr:
		if isCodeField(info, value) {
			return true
		}
		name = value.Sel
	default:
		return false
	}
	code, ok := info.Uses[name].(*types.Const)
	return ok && code.Pkg() != nil && code.Pkg().Path() == clierrorPkgPath && code.Exported() &&
		registered[constant.StringVal(code.Val())]
}

func isCodeField(info *types.Info, selector *ast.SelectorExpr) bool {
	selection, ok := info.Selections[selector]
	return ok && selection.Kind() == types.FieldVal && selection.Obj().Name() == codeFieldName &&
		isClierrorError(selection.Recv())
}

func isClierrorError(t types.Type) bool {
	if pointer, ok := t.(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == clierrorPkgPath && named.Obj().Name() == errorTypeName
}
