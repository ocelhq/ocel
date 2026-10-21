package sourcearchive_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "sourcearchive-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", home)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		os.Unsetenv(name)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
