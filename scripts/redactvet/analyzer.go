package main

import (
	"go/ast"
	"go/constant"
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

var Analyzer = &analysis.Analyzer{
	Name:      "redactvet",
	Doc:       "reports formatting a protobuf message that carries a debug_redact field, which protobuf-go renders in clear",
	Run:       run,
	FactTypes: []analysis.Fact{new(redacting)},
}

type redacting struct {
	Messages []redacted
}

type redacted struct {
	Full string
	Go   string
}

func (*redacting) AFact() {}

func (r *redacting) String() string {
	names := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Go != "" {
			names = append(names, m.Go)
		}
	}
	return "redacting(" + strings.Join(names, ", ") + ")"
}

var sinks = map[string]bool{
	"fmt":      true,
	"log":      true,
	"log/slog": true,
	"testing":  true,
	"google.golang.org/protobuf/encoding/prototext": true,
}

func run(pass *analysis.Pass) (any, error) {
	if err := describe(pass); err != nil {
		return nil, err
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				formatted(pass, node)
			case *ast.SelectorExpr:
				stringed(pass, node)
			}
			return true
		})
	}
	return nil, nil
}

func formatted(pass *analysis.Pass, call *ast.CallExpr) {
	callee := typeutil.Callee(pass.TypesInfo, call)
	if callee == nil || callee.Pkg() == nil || !sinks[callee.Pkg().Path()] {
		return
	}
	for _, arg := range call.Args {
		if t := pass.TypesInfo.TypeOf(arg); t != nil && redacts(pass, t, map[types.Type]bool{}) {
			report(pass, arg, t)
		}
	}
}

func stringed(pass *analysis.Pass, sel *ast.SelectorExpr) {
	selection, ok := pass.TypesInfo.Selections[sel]
	if !ok || sel.Sel.Name != "String" {
		return
	}
	method, ok := selection.Obj().(*types.Func)
	if !ok {
		return
	}
	if isMessage(pass, method.Signature().Recv().Type()) {
		report(pass, sel.X, selection.Recv())
	}
}

func report(pass *analysis.Pass, at ast.Expr, t types.Type) {
	pass.Reportf(at.Pos(), "%s renders its debug_redact fields in clear, since protobuf-go ignores the option: format the fields you need, never the message",
		types.TypeString(t, byName(pass.Pkg)))
}

func byName(self *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == self {
			return ""
		}
		return p.Name()
	}
}

func redacts(pass *analysis.Pass, t types.Type, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	if isMessage(pass, t) {
		return true
	}
	if renderer := rendering(t); renderer != nil {
		return isMessage(pass, renderer.Signature().Recv().Type())
	}
	switch t := types.Unalias(t).(type) {
	case *types.Pointer:
		return redacts(pass, t.Elem(), seen)
	case *types.Named:
		return redacts(pass, t.Underlying(), seen)
	case *types.Slice:
		return redacts(pass, t.Elem(), seen)
	case *types.Array:
		return redacts(pass, t.Elem(), seen)
	case *types.Map:
		return redacts(pass, t.Key(), seen) || redacts(pass, t.Elem(), seen)
	case *types.Struct:
		for field := range t.Fields() {
			if redacts(pass, field.Type(), seen) {
				return true
			}
		}
	}
	return false
}

func isMessage(pass *analysis.Pass, t types.Type) bool {
	if pointer, ok := types.Unalias(t).(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && carried(pass, named.Obj())
}

func rendering(t types.Type) *types.Func {
	for _, name := range []string{"Format", "Error", "String"} {
		if method, _, _ := types.LookupFieldOrMethod(t, false, nil, name); method != nil {
			if fn, ok := method.(*types.Func); ok {
				return fn
			}
		}
	}
	return nil
}

func carried(pass *analysis.Pass, obj *types.TypeName) bool {
	if obj.Pkg() == nil {
		return false
	}
	var fact redacting
	if !pass.ImportPackageFact(obj.Pkg(), &fact) {
		return false
	}
	return slices.ContainsFunc(fact.Messages, func(m redacted) bool { return m.Go == obj.Name() })
}

type message struct {
	desc     *descriptorpb.DescriptorProto
	goName   string
	declared *types.Const
}

func describe(pass *analysis.Pass) error {
	local := map[string]message{}
	scope := pass.Pkg.Scope()
	for _, name := range scope.Names() {
		if !strings.HasPrefix(name, "file_") || !strings.HasSuffix(name, "_rawDesc") {
			continue
		}
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || c.Val().Kind() != constant.String {
			continue
		}
		var file descriptorpb.FileDescriptorProto
		if err := proto.Unmarshal([]byte(constant.StringVal(c.Val())), &file); err != nil {
			return err
		}
		prefix := ""
		if file.GetPackage() != "" {
			prefix = "." + file.GetPackage()
		}
		collect(local, c, prefix, "", file.GetMessageType())
	}
	if len(local) == 0 {
		return nil
	}

	known := map[string]bool{}
	for _, fact := range pass.AllPackageFacts() {
		if r, ok := fact.Fact.(*redacting); ok {
			for _, m := range r.Messages {
				known[m.Full] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for full, m := range local {
			if !known[full] && carries(m.desc, known) {
				known[full] = true
				changed = true
			}
		}
	}

	fact := &redacting{}
	for full, m := range local {
		if !known[full] {
			continue
		}
		fact.Messages = append(fact.Messages, redacted{Full: full, Go: m.goName})
		if _, named := scope.Lookup(m.goName).(*types.TypeName); m.goName != "" && !named {
			pass.Reportf(m.declared.Pos(), "no Go type %s for %s, which carries a debug_redact field: protoc-gen-go no longer names messages the way redactvet expects", m.goName, full)
		}
	}
	slices.SortFunc(fact.Messages, func(a, b redacted) int { return strings.Compare(a.Full, b.Full) })
	if len(fact.Messages) > 0 {
		pass.ExportPackageFact(fact)
	}
	return nil
}

func collect(into map[string]message, declared *types.Const, prefix, goPrefix string, messages []*descriptorpb.DescriptorProto) {
	for _, desc := range messages {
		full := prefix + "." + desc.GetName()
		goName := goCamelCase(desc.GetName())
		if goPrefix != "" {
			goName = goPrefix + "_" + goName
		}
		entry := goName
		if desc.GetOptions().GetMapEntry() {
			entry = ""
		}
		into[full] = message{desc: desc, goName: entry, declared: declared}
		collect(into, declared, full, goName, desc.GetNestedType())
	}
}

func carries(desc *descriptorpb.DescriptorProto, known map[string]bool) bool {
	for _, field := range desc.GetField() {
		if field.GetOptions().GetDebugRedact() || known[field.GetTypeName()] {
			return true
		}
	}
	return false
}

func goCamelCase(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.' && i+1 < len(s) && isLower(s[i+1]):
		case c == '.':
			b = append(b, '_')
		case c == '_' && (i == 0 || s[i-1] == '.'):
			b = append(b, 'X')
		case c == '_' && i+1 < len(s) && isLower(s[i+1]):
		case '0' <= c && c <= '9':
			b = append(b, c)
		default:
			if isLower(c) {
				c -= 'a' - 'A'
			}
			b = append(b, c)
			for ; i+1 < len(s) && isLower(s[i+1]); i++ {
				b = append(b, s[i+1])
			}
		}
	}
	return string(b)
}

func isLower(c byte) bool { return 'a' <= c && c <= 'z' }
