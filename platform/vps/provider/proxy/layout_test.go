package proxy_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	contractPath  = "github.com/ocelhq/ocel/platform/vps/provider/proxy"
	proxyFile     = "proxy.go"
	assertionForm = "var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name"
)

func TestEachProxyIsOneImplementationWhoseMethodsAreAllInItsProxyGo(t *testing.T) {
	t.Parallel()

	for _, fault := range swept(t, ".", contract(t)) {
		t.Error(fault)
	}
}

func TestTheSweepSkipsWhatGoIgnores(t *testing.T) {
	t.Parallel()

	got := swept(t, "testdata/swept", contract(t))
	want := []string{"testdata/swept/bare has no proxy.go that declares anything; open one with var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name"}
	if !slices.Equal(got, want) {
		t.Errorf("faults in testdata/swept:\n got %q\nwant %q", got, want)
	}
}

func TestTheContractIsReadOnlyWhenItSpellsOutEveryMethod(t *testing.T) {
	t.Parallel()

	_, err := contractIn("testdata/embedded/proxy.go")
	want := "testdata/embedded/proxy.go embeds io.Closer in Proxy, whose methods the layout rule cannot read; declare them on Proxy itself"
	if err == nil || err.Error() != want {
		t.Errorf("contractIn(testdata/embedded/proxy.go) = %v, want %q", err, want)
	}
}

func TestTheLayoutRuleNamesWhatToMove(t *testing.T) {
	t.Parallel()

	methods := contract(t)
	cases := map[string][]string{
		"conforming":  nil,
		"valued":      nil,
		"constrained": nil,
		"unasserted": {
			"testdata/unasserted/proxy.go opens with func (*Unasserted).Guarantees, which is no assertion the layout rule reads; open it with var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name",
		},
		"generic": {
			"testdata/generic/proxy.go opens with var _ proxy.Proxy = (*Generic[int])(nil), which is no assertion the layout rule reads; open it with var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name",
		},
		"grouped": {
			"testdata/grouped/proxy.go opens with var (_ proxy.Proxy = (*Grouped)(nil); _ fmt.Stringer = (*Grouped)(nil)), which is no assertion the layout rule reads; open it with var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name",
		},
		"dotted": {
			"testdata/dotted/proxy.go opens with var _ Proxy = Dotted{}, which is no assertion the layout rule reads; open it with var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name",
		},
		"addressed": {
			"testdata/addressed/proxy.go opens with var _ proxy.Proxy = &Addressed{}, which is no assertion the layout rule reads; open it with var _ proxy.Proxy = (*T)(nil), or T{} for a value receiver, where T is a non-generic type and proxy is imported by name",
		},
		"twofold": {
			"testdata/twofold implements proxy.Proxy 2 times, as First and Second; move each into a subpackage of its own",
		},
		"covert": {
			"testdata/covert implements proxy.Proxy 2 times, as Covert and Overt; move each into a subpackage of its own",
		},
		"misnamed": {
			"testdata/misnamed/misnamed.go asserts proxy.Proxy for Misnamed; move the assertion and Misnamed's methods of proxy.Proxy to testdata/misnamed/proxy.go",
		},
		"late": {
			"testdata/late/proxy.go opens with func (*Late).Guarantees; move the assertion that Late implements proxy.Proxy above it",
		},
		"incomplete": {
			"testdata/incomplete/proxy.go declares no Certificate on Incomplete; declare it there",
		},
		"scattered": {
			"testdata/scattered/certificate.go declares func (*Scattered).Certificate, a method of proxy.Proxy; move it to testdata/scattered/proxy.go",
		},
		"crowded": {
			"testdata/crowded/proxy.go declares const upstream, which is no method of proxy.Proxy on Crowded; move it to another file",
			"testdata/crowded/proxy.go declares func (*Crowded).admit, which is no method of proxy.Proxy on Crowded; move it to another file",
		},
	}
	for fixture, want := range cases {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join("testdata", fixture)
			got := faults(dir, parsed(t, dir), methods)
			if !slices.Equal(got, want) {
				t.Errorf("faults in %s:\n got %q\nwant %q", fixture, got, want)
			}
		})
	}
}

func swept(t *testing.T, root string, methods []string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	var reported []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == "testdata" || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(root, name)
		if sources := parsed(t, dir); len(sources) > 0 {
			reported = append(reported, faults(dir, sources, methods)...)
		}
	}
	return reported
}

type source struct {
	name string
	file *ast.File
}

type assertion struct {
	source
	decl ast.Decl
	impl string
}

func faults(dir string, sources []source, methods []string) []string {
	var asserted []assertion
	for _, src := range sources {
		asserted = append(asserted, assertionsIn(src)...)
	}
	if impls := implementations(sources, asserted, methods); len(impls) > 1 {
		return []string{fmt.Sprintf("%s implements proxy.Proxy %d times, as %s; move each into a subpackage of its own",
			dir, len(impls), strings.Join(impls, " and "))}
	}
	if len(asserted) == 0 {
		return []string{unasserted(dir, sources)}
	}
	return misplaced(dir, asserted[0], sources, methods)
}

func unasserted(dir string, sources []source) string {
	var declared []ast.Decl
	if i := slices.IndexFunc(sources, func(src source) bool { return src.name == proxyFile }); i >= 0 {
		declared = declarations(sources[i].file)
	}
	if len(declared) == 0 {
		return fmt.Sprintf("%s has no proxy.go that declares anything; open one with %s", dir, assertionForm)
	}
	return fmt.Sprintf("%s opens with %s, which is no assertion the layout rule reads; open it with %s",
		filepath.Join(dir, proxyFile), described(declared[0]), assertionForm)
}

func implementations(sources []source, asserted []assertion, methods []string) []string {
	declared := map[string][]string{}
	for _, src := range sources {
		for _, decl := range src.file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil {
				impl := strings.TrimPrefix(receiver(fn), "*")
				declared[impl] = append(declared[impl], fn.Name.Name)
			}
		}
	}
	var impls []string
	for _, each := range asserted {
		impls = append(impls, each.impl)
	}
	for impl, names := range declared {
		if impl != "" && !slices.ContainsFunc(methods, func(method string) bool { return !slices.Contains(names, method) }) {
			impls = append(impls, impl)
		}
	}
	slices.Sort(impls)
	return slices.Compact(impls)
}

func misplaced(dir string, sole assertion, sources []source, methods []string) []string {
	at := filepath.Join(dir, sole.name)
	if sole.name != proxyFile {
		return []string{fmt.Sprintf("%s asserts proxy.Proxy for %s; move the assertion and %s's methods of proxy.Proxy to %s",
			at, sole.impl, sole.impl, filepath.Join(dir, proxyFile))}
	}
	declared := declarations(sole.file)
	var misplacements []string
	if first := declared[0]; first != sole.decl {
		misplacements = append(misplacements, fmt.Sprintf("%s opens with %s; move the assertion that %s implements proxy.Proxy above it",
			at, described(first), sole.impl))
	}
	for _, decl := range declared {
		if name, ok := methodOn(decl, sole.impl); decl == sole.decl || ok && slices.Contains(methods, name) {
			continue
		}
		for _, name := range spelled(decl) {
			misplacements = append(misplacements, fmt.Sprintf("%s declares %s, which is no method of proxy.Proxy on %s; move it to another file",
				at, name, sole.impl))
		}
	}
	for _, method := range methods {
		if declares(sole.file, sole.impl, method) != nil {
			continue
		}
		strays := 0
		for _, src := range sources {
			if src.name == proxyFile {
				continue
			}
			if stray := declares(src.file, sole.impl, method); stray != nil {
				strays++
				misplacements = append(misplacements, fmt.Sprintf("%s declares %s, a method of proxy.Proxy; move it to %s",
					filepath.Join(dir, src.name), spelled(stray)[0], at))
			}
		}
		if strays == 0 {
			misplacements = append(misplacements, fmt.Sprintf("%s declares no %s on %s; declare it there", at, method, sole.impl))
		}
	}
	return misplacements
}

func declares(file *ast.File, impl, method string) ast.Decl {
	for _, decl := range file.Decls {
		if name, ok := methodOn(decl, impl); ok && name == method {
			return decl
		}
	}
	return nil
}

func methodOn(decl ast.Decl, impl string) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv == nil || strings.TrimPrefix(receiver(fn), "*") != impl {
		return "", false
	}
	return fn.Name.Name, true
}

func contract(t *testing.T) []string {
	t.Helper()
	methods, err := contractIn(proxyFile)
	if err != nil {
		t.Fatal(err)
	}
	return methods
}

func contractIn(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse the contract: %w", err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			named := spec.(*ast.TypeSpec)
			declared, ok := named.Type.(*ast.InterfaceType)
			if !ok || named.Name.Name != "Proxy" {
				continue
			}
			var methods []string
			for _, field := range declared.Methods.List {
				if len(field.Names) == 0 {
					return nil, fmt.Errorf("%s embeds %s in Proxy, whose methods the layout rule cannot read; declare them on Proxy itself",
						path, types.ExprString(field.Type))
				}
				for _, name := range field.Names {
					methods = append(methods, name.Name)
				}
			}
			return methods, nil
		}
	}
	return nil, fmt.Errorf("%s declares no Proxy interface", path)
}

func declarations(file *ast.File) []ast.Decl {
	return slices.DeleteFunc(slices.Clone(file.Decls), func(decl ast.Decl) bool {
		gen, ok := decl.(*ast.GenDecl)
		return ok && gen.Tok == token.IMPORT
	})
}

func described(decl ast.Decl) string {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR {
		return strings.Join(spelled(decl), ", ")
	}
	specs := make([]string, 0, len(gen.Specs))
	for _, spec := range gen.Specs {
		specs = append(specs, valued(spec.(*ast.ValueSpec)))
	}
	if gen.Lparen.IsValid() {
		return "var (" + strings.Join(specs, "; ") + ")"
	}
	return "var " + specs[0]
}

func valued(spec *ast.ValueSpec) string {
	var names, values []string
	for _, name := range spec.Names {
		names = append(names, name.Name)
	}
	for _, value := range spec.Values {
		values = append(values, types.ExprString(value))
	}
	written := strings.Join(names, ", ")
	if spec.Type != nil {
		written += " " + types.ExprString(spec.Type)
	}
	if len(values) > 0 {
		written += " = " + strings.Join(values, ", ")
	}
	return written
}

func spelled(decl ast.Decl) []string {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		if decl.Recv == nil {
			return []string{"func " + decl.Name.Name}
		}
		return []string{"func (" + receiver(decl) + ")." + decl.Name.Name}
	case *ast.GenDecl:
		var names []string
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, "type "+spec.Name.Name)
			case *ast.ValueSpec:
				for _, name := range spec.Names {
					names = append(names, decl.Tok.String()+" "+name.Name)
				}
			}
		}
		return names
	default:
		return nil
	}
}

func receiver(decl *ast.FuncDecl) string {
	switch named := decl.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := named.X.(*ast.Ident); ok {
			return "*" + ident.Name
		}
	case *ast.Ident:
		return named.Name
	}
	return ""
}

func parsed(t *testing.T, dir string) []source {
	t.Helper()
	pkg, err := build.Default.ImportDir(dir, 0)
	var nothing *build.NoGoError
	if errors.As(err, &nothing) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var sources []source
	for _, name := range slices.Concat(pkg.GoFiles, pkg.CgoFiles) {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Join(dir, name), err)
		}
		sources = append(sources, source{name: name, file: file})
	}
	return sources
}

func assertionsIn(src source) []assertion {
	alias, imported := contractAlias(src.file)
	if !imported {
		return nil
	}
	var asserted []assertion
	for _, decl := range src.file.Decls {
		if impl, ok := assertedBy(decl, alias); ok {
			asserted = append(asserted, assertion{source: src, decl: decl, impl: impl})
		}
	}
	return asserted
}

func contractAlias(file *ast.File) (string, bool) {
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil || path != contractPath {
			continue
		}
		if imported.Name != nil {
			return imported.Name.Name, true
		}
		return filepath.Base(contractPath), true
	}
	return "", false
}

func assertedBy(decl ast.Decl, alias string) (string, bool) {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.VAR || len(gen.Specs) != 1 {
		return "", false
	}
	spec, ok := gen.Specs[0].(*ast.ValueSpec)
	if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "_" || len(spec.Values) != 1 {
		return "", false
	}
	named, ok := spec.Type.(*ast.SelectorExpr)
	if !ok || named.Sel.Name != "Proxy" {
		return "", false
	}
	if pkg, ok := named.X.(*ast.Ident); !ok || pkg.Name != alias {
		return "", false
	}
	return implemented(spec.Values[0])
}

func implemented(value ast.Expr) (string, bool) {
	switch value := value.(type) {
	case *ast.CompositeLit:
		named, ok := value.Type.(*ast.Ident)
		if !ok || len(value.Elts) != 0 {
			return "", false
		}
		return named.Name, true
	case *ast.CallExpr:
		if len(value.Args) != 1 {
			return "", false
		}
		if nothing, ok := value.Args[0].(*ast.Ident); !ok || nothing.Name != "nil" {
			return "", false
		}
		wrapped, ok := value.Fun.(*ast.ParenExpr)
		if !ok {
			return "", false
		}
		pointer, ok := wrapped.X.(*ast.StarExpr)
		if !ok {
			return "", false
		}
		named, ok := pointer.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		return named.Name, true
	default:
		return "", false
	}
}
