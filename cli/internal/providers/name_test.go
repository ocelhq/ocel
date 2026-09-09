package providers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestANameThatIsNotOneSegmentNeverReachesAPath(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "..", "../..", "aws/../../etc", "aws/deploy", `aws\deploy`, "AWS", "aws_1"} {
		store := &Store{
			Dir:      t.TempDir(),
			Version:  testVersion,
			Platform: Platform{GOOS: "linux", GOARCH: "amd64"},
		}
		_, err := store.Binary(context.Background(), name, "deadbeef")
		if err == nil {
			t.Errorf("Binary(%q) error = nil, want the name refused before it is joined into a path", name)
			continue
		}
		if !strings.Contains(err.Error(), "is not a provider name") {
			t.Errorf("Binary(%q) error = %q, want the name refused by the store", name, err.Error())
		}
	}
}

func TestAnEscapingNameIsRefusedAgainstADirectoryOfProviders(t *testing.T) {
	t.Parallel()

	override := t.TempDir()
	outside := filepath.Join(override, "..", "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	store := &Store{
		Dir:      t.TempDir(),
		Override: override,
		Version:  testVersion,
		Platform: Platform{GOOS: "linux", GOARCH: "amd64"},
	}
	if _, err := store.Binary(context.Background(), "../outside", "deadbeef"); err == nil {
		t.Fatal("Binary() error = nil, want a name climbing out of the providers directory refused")
	}
}

func TestTheNamesTheReleaseShipsAreNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"aws", "gcp", "vps", "a-second-cloud"} {
		if err := checkName(name); err != nil {
			t.Errorf("checkName(%q) = %v, want a name the store resolves", name, err)
		}
	}
}
