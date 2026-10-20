package functioncode_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
)

func artifactOf(t *testing.T, name string, size int) functioncode.Upload {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".zip")
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return functioncode.Upload{
		Name: name,
		Ref:  provider.ArtifactRef{Tier: "production", Key: name + ".zip"},
		Path: path,
	}
}

func TestEachUploadNamesItsFunctionAndHowLargeItsArtifactIs(t *testing.T) {
	t.Parallel()

	progress := &fake.Log{}
	uploads := []functioncode.Upload{
		artifactOf(t, "api", 3<<20+1<<19),
		artifactOf(t, "cron", 2048),
		artifactOf(t, "ping", 300),
	}

	if err := functioncode.Ship(context.Background(), fake.NewArtifacts(), uploads, progress); err != nil {
		t.Fatalf("Ship() = %v", err)
	}
	got := progress.Lines()
	slices.Sort(got)
	want := []string{
		"INFO Uploading function api's artifact (3.5 MiB)",
		"INFO Uploading function cron's artifact (2.0 KiB)",
		"INFO Uploading function ping's artifact (300 B)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Ship() said %q, want %q: a bare name says neither what is uploading nor how much of it", got, want)
	}
}
