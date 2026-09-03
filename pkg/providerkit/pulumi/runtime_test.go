package pulumi

import (
	"os"
	"path/filepath"
	"testing"
)

func stagedRuntime(t *testing.T, dir, marker string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "pulumi"), []byte(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSettleMovesTheStagedRuntimeIntoPlace(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "3.146.0")
	staging := stagedRuntime(t, root+"-1", "mine")

	if err := settle(staging, root); err != nil {
		t.Fatalf("settle() = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "bin", "pulumi")); string(got) != "mine" {
		t.Errorf("the root holds %q, want the staged runtime", got)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("the staging dir stands after settle: %v", err)
	}
}

func TestSettleYieldsToARuntimeAnotherProcessSettledFirst(t *testing.T) {
	base := t.TempDir()
	root := stagedRuntime(t, filepath.Join(base, "3.146.0"), "theirs")
	staging := stagedRuntime(t, root+"-2", "mine")

	if err := settle(staging, root); err != nil {
		t.Fatalf("settle() against a settled root = %v, want it to yield", err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "bin", "pulumi")); string(got) != "theirs" {
		t.Errorf("the root holds %q; the loser overwrote a runtime another process may be running", got)
	}
}
