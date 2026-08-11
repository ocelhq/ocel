package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		t.Parallel()

		err := runBootstrap(context.Background(), defaultDeps(), t.TempDir(), bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runBootstrap(context.Background(), defaultDeps(), root, bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runBootstrap err = nil, want error")
		}
	})
}
