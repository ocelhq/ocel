package confighome

import (
	"path/filepath"
	"runtime"
	"testing"
)

func Set(home string, setenv func(name, value string)) string {
	if runtime.GOOS == "darwin" {
		setenv("HOME", home)
		return filepath.Join(home, "Library", "Application Support", "ocel")
	}
	setenv("XDG_CONFIG_HOME", home)
	setenv("AppData", home)
	return filepath.Join(home, "ocel")
}

func Isolate(t *testing.T) string {
	t.Helper()
	return Set(t.TempDir(), t.Setenv)
}
