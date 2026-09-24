package main

import (
	"go/ast"
	"go/constant"
	"go/types"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

var Analyzer = &analysis.Analyzer{
	Name:      "redactvet",
	Doc:       "reports formatting or JSON-encoding a protobuf message that carries a debug_redact field, which protobuf-go renders in clear",
	Run:       run,
	FactTypes: []analysis.Fact{new(carriers)},
}

type carriers struct {
	Messages []carrier
}

type carrier struct {
	Full string
	Go   string
}

func (*carriers) AFact() {}

func (r *carriers) String() string {
	names := make([]string, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.Go != "" {
			names = append(names, m.Go)
		}
	}
	return "carriers(" + strings.Join(names, ", ") + ")"
}

type sink int

const (
	formats sink = 1 << iota
	encodes
)

var sinks = map[string]sink{
	"fmt":     formats,
	"log":     formats,
	"testing": formats,
	"google.golang.org/protobuf/encoding/prototext": formats,
	"log/slog": formats | encodes,
}

var jsonEncoders = map[string]bool{"Marshal": true, "MarshalIndent": true, "Encode": true}

func sinkOf(callee *types.Func) sink {
	if callee.Pkg().Path() == "encoding/json" {
		if jsonEncoders[callee.Name()] {
			return encodes
		}
		return 0
	}
	return sinks[callee.Pkg().Path()]
}

func run(pass *analysis.Pass) (any, error) {
	if err := exportCarriers(pass); err != nil {
		return nil, err
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				checkSink(pass, node)
			case *ast.SelectorExpr:
				checkString(pass, node)
			}
			return true
		})
	}
	return nil, nil
}

func checkSink(pass *analysis.Pass, call *ast.CallExpr) {
	callee, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if !ok || callee.Pkg() == nil {
		return
	}
	kind := sinkOf(callee)
	for _, arg := range call.Args {
		t := pass.TypesInfo.TypeOf(arg)
		switch {
		case t == nil:
		case kind&formats != 0 && fmtRenders(pass, t):
			report(pass, arg, t)
		case kind&encodes != 0 && jsonEncodes(pass, t, false, map[addressed]bool{}):
			pass.Reportf(arg.Pos(), "%s encodes its debug_redact fields in clear through encoding/json: encode the fields you need, and a message bound for the wire with protojson",
				types.TypeString(t, byName(pass.Pkg)))
		}
	}
}

func checkString(pass *analysis.Pass, sel *ast.SelectorExpr) {
	selection, ok := pass.TypesInfo.Selections[sel]
	if !ok || sel.Sel.Name != "String" {
		return
	}
	method, ok := selection.Obj().(*types.Func)
	if !ok {
		return
	}
	if isCarrier(pass, method.Signature().Recv().Type()) {
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

type reach int

const (
	methodsCalled reach = iota
	embeddedUnexported
	unexported
)

func (r reach) through(field *types.Var) reach {
	switch {
	case r == unexported || !field.Exported() && !field.Embedded():
		return unexported
	case !field.Exported():
		return embeddedUnexported
	}
	return methodsCalled
}

func (r reach) elements() reach {
	if r == methodsCalled {
		return methodsCalled
	}
	return unexported
}

type visit struct {
	t     types.Type
	top   bool
	reach reach
}

func fmtRenders(pass *analysis.Pass, t types.Type) bool {
	return fmtReaches(pass, t, true, methodsCalled, map[visit]bool{})
}

func fmtReaches(pass *analysis.Pass, t types.Type, top bool, r reach, seen map[visit]bool) bool {
	if seen[visit{t, top, r}] {
		return false
	}
	seen[visit{t, top, r}] = true
	if isGenericMessage(t) {
		return r == methodsCalled
	}
	if r == methodsCalled {
		if method := renderer(t); method != nil {
			return isCarrier(pass, method.Signature().Recv().Type())
		}
	}
	if named, ok := types.Unalias(t).(*types.Named); ok && namesCarrier(pass, named.Obj()) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		if !top {
			return false
		}
		switch u.Elem().Underlying().(type) {
		case *types.Struct, *types.Array, *types.Slice, *types.Map:
			return fmtReaches(pass, u.Elem(), false, r, seen)
		}
	case *types.Struct:
		for field := range u.Fields() {
			if fmtReaches(pass, field.Type(), false, r.through(field), seen) {
				return true
			}
		}
	case *types.Slice:
		return fmtReaches(pass, u.Elem(), false, r.elements(), seen)
	case *types.Array:
		return fmtReaches(pass, u.Elem(), false, r.elements(), seen)
	case *types.Map:
		return fmtReaches(pass, u.Key(), false, r.elements(), seen) || fmtReaches(pass, u.Elem(), false, r.elements(), seen)
	}
	return false
}

type addressed struct {
	t           types.Type
	addressable bool
}

func jsonEncodes(pass *analysis.Pass, t types.Type, addressable bool, seen map[addressed]bool) bool {
	if seen[addressed{t, addressable}] {
		return false
	}
	seen[addressed{t, addressable}] = true
	if isGenericMessage(t) || isCarrier(pass, t) {
		return true
	}
	if marshalsItself(t) || addressable && marshalsItself(types.NewPointer(t)) {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return jsonEncodes(pass, u.Elem(), true, seen)
	case *types.Slice:
		return jsonEncodes(pass, u.Elem(), true, seen)
	case *types.Array:
		return jsonEncodes(pass, u.Elem(), addressable, seen)
	case *types.Map:
		return jsonEncodes(pass, u.Elem(), false, seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if field := u.Field(i); !jsonSkips(field, u.Tag(i)) && jsonEncodes(pass, field.Type(), addressable, seen) {
				return true
			}
		}
	}
	return false
}

func jsonSkips(field *types.Var, tag string) bool {
	if reflect.StructTag(tag).Get("json") == "-" {
		return true
	}
	if field.Exported() {
		return false
	}
	if !field.Embedded() {
		return true
	}
	t := field.Type()
	if pointer, ok := t.Underlying().(*types.Pointer); ok {
		t = pointer.Elem()
	}
	_, isStruct := t.Underlying().(*types.Struct)
	return !isStruct
}

func marshalsItself(t types.Type) bool {
	methods := types.NewMethodSet(t)
	return methods.Lookup(nil, "MarshalJSON") != nil || methods.Lookup(nil, "MarshalText") != nil
}

func isCarrier(pass *analysis.Pass, t types.Type) bool {
	if pointer, ok := types.Unalias(t).(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && namesCarrier(pass, named.Obj())
}

func isGenericMessage(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "google.golang.org/protobuf/reflect/protoreflect" && named.Obj().Name() == "ProtoMessage"
}

func renderer(t types.Type) *types.Func {
	methods := types.NewMethodSet(t)
	for _, name := range []string{"Format", "Error", "String"} {
		if selection := methods.Lookup(nil, name); selection != nil {
			if fn, ok := selection.Obj().(*types.Func); ok {
				return fn
			}
		}
	}
	return nil
}

func namesCarrier(pass *analysis.Pass, obj *types.TypeName) bool {
	if obj.Pkg() == nil {
		return false
	}
	var fact carriers
	if !pass.ImportPackageFact(obj.Pkg(), &fact) {
		return false
	}
	return slices.ContainsFunc(fact.Messages, func(m carrier) bool { return m.Go == obj.Name() })
}

type message struct {
	desc     *descriptorpb.DescriptorProto
	goName   string
	declared *types.Const
}

func exportCarriers(pass *analysis.Pass) error {
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
		collectMessages(local, c, prefix, "", file.GetMessageType())
	}
	if len(local) == 0 {
		return nil
	}

	known := map[string]bool{}
	for _, fact := range pass.AllPackageFacts() {
		if r, ok := fact.Fact.(*carriers); ok {
			for _, m := range r.Messages {
				known[m.Full] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for full, m := range local {
			if !known[full] && holdsRedactedField(m.desc, known) {
				known[full] = true
				changed = true
			}
		}
	}

	fact := &carriers{}
	for full, m := range local {
		if !known[full] {
			continue
		}
		fact.Messages = append(fact.Messages, carrier{Full: full, Go: m.goName})
		if _, named := scope.Lookup(m.goName).(*types.TypeName); m.goName != "" && !named {
			pass.Reportf(m.declared.Pos(), "no Go type %s for %s, which carries a debug_redact field: protoc-gen-go no longer names messages the way redactvet expects", m.goName, full)
		}
	}
	slices.SortFunc(fact.Messages, func(a, b carrier) int { return strings.Compare(a.Full, b.Full) })
	if len(fact.Messages) > 0 {
		pass.ExportPackageFact(fact)
	}
	return nil
}

func collectMessages(into map[string]message, declared *types.Const, prefix, goPrefix string, messages []*descriptorpb.DescriptorProto) {
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
		collectMessages(into, declared, full, goName, desc.GetNestedType())
	}
}

func holdsRedactedField(desc *descriptorpb.DescriptorProto, known map[string]bool) bool {
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
