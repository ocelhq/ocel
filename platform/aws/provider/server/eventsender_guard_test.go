package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDeployNeverCallsStreamSendDirectly(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}

	var deployDecl *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Deploy" || fn.Recv == nil {
			continue
		}
		deployDecl = fn
		break
	}
	if deployDecl == nil {
		t.Fatal("did not find func (s *Server) Deploy in server.go")
	}

	ast.Inspect(deployDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Send" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if recv.Name == "stream" {
			t.Errorf("Deploy calls stream.Send directly at %s; every event must go through eventSender.send so the stream is never written from more than one goroutine", fset.Position(call.Pos()))
		}
		return true
	})
}
