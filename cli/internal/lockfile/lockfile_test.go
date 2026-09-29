package lockfile

import (
	"os"
	"path/filepath"
	"testing"
)

var pinned = Lock{
	CLI:        "0.2.0",
	Providers:  map[string]map[string]string{"fake": {"linux-amd64": "0003", "darwin-arm64": "0002"}},
	Connectors: map[string]map[string]string{"fake": {"linux-amd64": "0007"}},
}

func rendered(t *testing.T, lock Lock) []byte {
	t.Helper()
	raw, err := lock.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return raw
}

func TestTheLockReadsBackTheBytesItWrote(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	written := pinned
	if err := Write(dir, written); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		t.Fatalf("read %s: %v", Name, err)
	}
	if raw[len(raw)-1] != '\n' {
		t.Error("the lock does not end in a newline")
	}

	read, found, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !found {
		t.Fatal("Read found no lock where Write had just put one")
	}
	if a, b := rendered(t, read), rendered(t, written); string(a) != string(b) {
		t.Fatalf("the lock read back differs:\n%s\nwant\n%s", a, b)
	}
}

func TestNoLockReadsAsNoLock(t *testing.T) {
	t.Parallel()

	_, found, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if found {
		t.Fatal("Read claimed a lock in a directory containing none")
	}
}

func TestAFailedWriteLeavesThePriorLockIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a directory it has no write bit on")
	}

	dir := t.TempDir()
	if err := Write(dir, pinned); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}

	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("seal the directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := Write(dir, Lock{CLI: "0.3.0"}); err == nil {
		t.Fatal("Write() error = nil, want a write that cannot land to fail")
	}

	after, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatalf("read the lock back: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("a failed write changed the lock:\n%s\nwant\n%s", after, before)
	}
}
