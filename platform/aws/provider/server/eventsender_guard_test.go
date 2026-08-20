package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

var streamingHandlers = map[string]string{
	"server.go":         "Deploy",
	"server_destroy.go": "RemoveProject",
	"server_prune.go":   "RemoveStalePromotions",
	"server_preview.go": "RemovePreview",
}

func TestStreamingHandlersNeverCallStreamSendDirectly(t *testing.T) {
	t.Parallel()

	for file, fn := range streamingHandlers {
		t.Run(fn, func(t *testing.T) {
			t.Parallel()
			checkNoDirectStreamSend(t, file, fn)
		})
	}
}

func checkNoDirectStreamSend(t *testing.T, filename, fnName string) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	var decl *ast.FuncDecl
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName || fn.Recv == nil {
			continue
		}
		decl = fn
		break
	}
	if decl == nil {
		t.Fatalf("did not find func (s *Server) %s in %s", fnName, filename)
	}

	ast.Inspect(decl.Body, func(n ast.Node) bool {
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
			t.Errorf("%s calls stream.Send directly at %s; every event must go through eventSender.send so the stream is never written from more than one goroutine", fnName, fset.Position(call.Pos()))
		}
		return true
	})
}
