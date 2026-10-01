package terminal

import (
	"path/filepath"
	"testing"
)

func TestALogUnderAFolderStartingWithTwoDotsIsNamedRelativeToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if got, want := relLog(filepath.Join(dir, "..logs", "run.log")), filepath.Join("..logs", "run.log"); got != want {
		t.Errorf("relLog = %q, want %q", got, want)
	}
	above := filepath.Join(filepath.Dir(dir), "run.log")
	if got := relLog(above); got != above {
		t.Errorf("relLog of a log above the working directory = %q, want its absolute path %q", got, above)
	}
}
