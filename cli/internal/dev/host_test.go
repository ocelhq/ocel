package dev

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/dotfile"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestTheAppsOriginsFollowThePortInTheDotfile(t *testing.T) {
	root := t.TempDir()
	origins := appOrigins(root, valueSource{id: "dotenv", ownStore: true})

	clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "PORT=4100\n")
	if got := origins(); !slices.Contains(got, "http://localhost:4100") || !slices.Contains(got, "http://127.0.0.1:4100") {
		t.Fatalf("origins = %v, want the app on port 4100", got)
	}
	clitest.WriteFile(t, filepath.Join(root, dotfile.FileName), "PORT=4200\n")
	if got := origins(); !slices.Contains(got, "http://localhost:4200") || slices.Contains(got, "http://localhost:4100") {
		t.Fatalf("origins = %v after the port moved to 4200", got)
	}
}
