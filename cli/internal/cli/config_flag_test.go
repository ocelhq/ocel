package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestExplicitConfigPath(t *testing.T) {
	orig := configFlag
	t.Cleanup(func() { configFlag = orig })

	flag := rootCmd.PersistentFlags().Lookup("config")
	if flag == nil {
		t.Fatal("`ocel` does not accept --config")
	}
	if flag.Shorthand != "c" {
		t.Errorf("--config shorthand = %q, want %q", flag.Shorthand, "c")
	}

	t.Run("neither the flag nor the env is set", func(t *testing.T) {
		configFlag = ""
		if got := explicitConfigPath(); got != "" {
			t.Errorf("explicitConfigPath() = %q, want the empty path that leaves discovery alone", got)
		}
	})

	t.Run("the env alone is honoured", func(t *testing.T) {
		t.Setenv("OCEL_CONFIG", "from-env.ts")
		configFlag = ""
		if got := explicitConfigPath(); got != "from-env.ts" {
			t.Errorf("explicitConfigPath() = %q, want %q", got, "from-env.ts")
		}
	})

	t.Run("the flag alone is honoured", func(t *testing.T) {
		configFlag = "from-flag.ts"
		if got := explicitConfigPath(); got != "from-flag.ts" {
			t.Errorf("explicitConfigPath() = %q, want %q", got, "from-flag.ts")
		}
	})

	t.Run("the flag wins over the env", func(t *testing.T) {
		t.Setenv("OCEL_CONFIG", "from-env.ts")
		configFlag = "from-flag.ts"
		if got := explicitConfigPath(); got != "from-flag.ts" {
			t.Errorf("explicitConfigPath() = %q, want --config to win over OCEL_CONFIG", got)
		}
	})
}

func TestConfigFlagPathThatNamesNothingRefuses(t *testing.T) {
	orig := configFlag
	t.Cleanup(func() { configFlag = orig })

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

	configFlag = filepath.Join(".", "nope.ts")
	err := runBuild(context.Background(), newDeps(), root, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("runBuild err = nil, want a refusal for a --config path that names nothing")
	}
	if !strings.Contains(err.Error(), filepath.Join(root, "nope.ts")) {
		t.Fatalf("runBuild err = %v, want it to name the path --config asked for", err)
	}
}
