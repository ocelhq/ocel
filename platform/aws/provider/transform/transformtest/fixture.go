package transformtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", ".."))
}

func ExampleModule(t *testing.T, example, module string) string {
	t.Helper()
	path := filepath.Join(repo(t), "examples", example, filepath.FromSlash(module))
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the %s example module: %v", example, err)
	}
	return string(source)
}

func Root(t *testing.T, modules map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}

	pkg := filepath.Join(repo(t), "packages", "provider-aws")
	if _, err := os.Stat(filepath.Join(pkg, "package.json")); err != nil {
		t.Skipf("the provider-aws package is not checked out: %v", err)
	}

	root := t.TempDir()
	scope := filepath.Join(root, "node_modules", "@ocel")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatalf("create node_modules: %v", err)
	}
	if err := os.Symlink(pkg, filepath.Join(scope, "provider-aws")); err != nil {
		t.Fatalf("link the provider package: %v", err)
	}
	for name, source := range modules {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}
