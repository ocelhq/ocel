package content_test

import (
	"context"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing/content"
)

func TestAContextIsOmittedOnlyOnceMarkedSo(t *testing.T) {
	if content.IsOmitted(context.Background()) {
		t.Error("IsOmitted(an unmarked context) = true")
	}
	if !content.IsOmitted(content.Omit(context.Background())) {
		t.Error("IsOmitted(a marked context) = false")
	}
}

func TestThePackageImportsNothingButTheStandardLibrary(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				t.Errorf("%s imports %s, want only the standard library so a package that must not link the wire can mark its calls", source, path)
			}
		}
	}
}
