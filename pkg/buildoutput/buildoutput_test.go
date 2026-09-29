package buildoutput_test

import (
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestTheOutputRootOfAProjectDirectoryThatIsNotAbsoluteIsRefused(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{"", ".", "project"} {
		if root, err := buildoutput.Root(dir); err == nil {
			t.Errorf("Root(%q) = %q, want an error: a relative root resolves against whatever directory the process runs in", dir, root)
		}
	}
}

func TestTheOutputRootSitsInsideTheProjectDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	root, err := buildoutput.Root(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, filepath.FromSlash(buildoutput.Dir)); root != want {
		t.Errorf("Root(%q) = %q, want %q", dir, root, want)
	}
}
