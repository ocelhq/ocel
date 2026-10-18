package previewid

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		os.Unsetenv(name)
	}
	os.Exit(m.Run())
}
