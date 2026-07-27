package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBootstrap_MissingConfig_ErrorsBeforeAnySpawn(t *testing.T) {
	err := runBootstrap(context.Background(), t.TempDir(), bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runBootstrap err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %v, want it to hint at `ocel init`", err)
	}
}

func TestRunBootstrap_NoProviderConfigured_ErrorsBeforeAnySpawn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  projectId: "proj_no_provider",
};
`)

	err := runBootstrap(context.Background(), root, bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runBootstrap err = nil, want error")
	}
}
