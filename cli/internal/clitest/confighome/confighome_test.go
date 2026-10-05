package confighome_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
)

func TestIsolateMovesTheUserConfigDirectoryIntoATempHome(t *testing.T) {
	before, _ := os.UserConfigDir()
	dir := confighome.Isolate(t)
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "ocel"); dir != want {
		t.Errorf("Isolate() = %q, want %q", dir, want)
	}
	if base == before {
		t.Errorf("user config directory is still %q", base)
	}
}
