package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/credentials"
)

func TestRunBootstrap_NotLoggedIn_ReturnsExitErrorWithLoginInstruction(t *testing.T) {
	prev := loadCredentials
	loadCredentials = func() (credentials.Credentials, error) {
		return credentials.Credentials{}, credentials.ErrNotLoggedIn
	}
	defer func() { loadCredentials = prev }()

	var stderr bytes.Buffer
	err := runBootstrap(context.Background(), t.TempDir(), bootstrapOptions{}, &bytes.Buffer{}, &stderr, strings.NewReader(""))

	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runBootstrap err = %v (%T), want *ExitError", err, err)
	}
	if !strings.Contains(stderr.String(), "ocel login") {
		t.Fatalf("stderr = %q, want it to mention `ocel login`", stderr.String())
	}
}

func TestRunBootstrap_MissingConfig_ErrorsBeforeAnySpawn(t *testing.T) {
	setLoggedIn(t)

	err := runBootstrap(context.Background(), t.TempDir(), bootstrapOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runBootstrap err = nil, want error")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %v, want it to hint at `ocel init`", err)
	}
}

func TestRunBootstrap_NoProviderConfigured_ErrorsBeforeAnySpawn(t *testing.T) {
	setLoggedIn(t)

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
