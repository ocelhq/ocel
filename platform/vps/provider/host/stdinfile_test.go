package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAFileWrittenFromStdinReadsTheInheritedDescriptorRatherThanReopeningDevStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handed", "ocel-prod-db.env")
	command := writtenFromStdin(path, true)
	if strings.Contains(command, "/dev/stdin") {
		t.Errorf("%q reopens /dev/stdin, which a login's ssh pipe refuses to the state owner sudo acts as", command)
	}

	run := exec.Command("sh", "-c", command)
	run.Stdin = strings.NewReader("POSTGRES_PASSWORD=s3cret\n")
	if said, err := run.CombinedOutput(); err != nil {
		t.Fatalf("%q: %v\n%s", command, err, said)
	}
	written, err := os.ReadFile(path)
	if err != nil || string(written) != "POSTGRES_PASSWORD=s3cret\n" {
		t.Fatalf("the file holds %q, %v; want what stdin carried", written, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the file's mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
}

func TestAFileWrittenFromStdinOverAMoreOpenOneIsLeftReadableByItsOwnerAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ocel-prod-db.env")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("sh", "-c", writtenFromStdin(path, false))
	run.Stdin = strings.NewReader("new")
	if said, err := run.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, said)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the file's mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
}
