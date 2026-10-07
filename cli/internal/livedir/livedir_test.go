package livedir

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteHandsEachValueAsAPrivateFileUnderItsKey(t *testing.T) {
	parent := t.TempDir()
	dir, err := Write(parent, "ocel-live-", map[string]string{"STRIPE_API_KEY": "sk_live", "SESSION_SECRET": "ss_live"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	t.Cleanup(func() { _ = Remove(dir) })

	if filepath.Dir(dir) != parent || !strings.HasPrefix(filepath.Base(dir), "ocel-live-") {
		t.Errorf("dir = %s, want an ocel-live-* dir under %s", dir, parent)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", info.Mode().Perm())
	}
	for key, want := range map[string]string{"STRIPE_API_KEY": "sk_live", "SESSION_SECRET": "ss_live"} {
		path := filepath.Join(dir, key)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want {
			t.Errorf("%s = %q, want %q", key, body, want)
		}
		file, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if file.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", key, file.Mode().Perm())
		}
	}
}

func TestWriteRefusesAKeyThatNamesNoPlainFileInTheDir(t *testing.T) {
	for _, key := range []string{"", ".", "..", ".hidden", "../ESCAPED", "a/b", `a\b`, "/abs"} {
		parent := t.TempDir()
		_, err := Write(parent, "ocel-live-", map[string]string{key: "x"})
		if err == nil || !strings.Contains(err.Error(), "rename it where it is declared") {
			t.Errorf("Write(%q) err = %v, want a refusal that says to rename the key", key, err)
		}
		if entries, _ := os.ReadDir(parent); len(entries) != 0 {
			t.Errorf("Write(%q) left %d entries under %s, want nothing created for a refused key", key, len(entries), parent)
		}
	}
}

func TestAWriteThatFailsPartWayLeavesNoValueOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file name too long for the filesystem is how this test fails a write part way, and windows reports it differently")
	}
	parent := t.TempDir()
	_, err := Write(parent, "ocel-live-", map[string]string{
		"A_WRITTEN_KEY":           "written",
		strings.Repeat("K", 4096): "unwritable",
	})
	if err == nil {
		t.Fatal("Write err = nil, want the failed write")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed write left %v under %s, want the dir and every value it wrote removed", entries, parent)
	}
}

func TestRemoveRecordedRemovesEveryDirWrittenAndNotYetRemoved(t *testing.T) {
	parent := t.TempDir()
	kept, err := Write(parent, "ocel-live-", map[string]string{"KEY": "value"})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := Write(parent, "ocel-live-", map[string]string{"KEY": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Remove(removed); err != nil {
		t.Fatal(err)
	}

	RemoveRecorded()

	for _, dir := range []string{kept, removed} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat %s after RemoveAll: err = %v, want it gone", dir, err)
		}
	}
}

func TestRemovingADirRemovesTheDirsWrittenInsideIt(t *testing.T) {
	root, err := Create(t.TempDir(), "ocel-dev-live-")
	if err != nil {
		t.Fatal(err)
	}
	inner, err := Write(root, "bindings-", map[string]string{"KEY": "value"})
	if err != nil {
		t.Fatal(err)
	}

	if err := Remove(root); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(inner); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s after removing %s: err = %v, want it gone", inner, root, err)
	}
}
