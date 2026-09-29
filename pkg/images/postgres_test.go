package images

import (
	"slices"
	"strings"
	"testing"
)

func TestEveryPostgresVersionIsPinnedByDigest(t *testing.T) {
	t.Parallel()

	if got, want := PostgresVersions(), []string{"14", "15", "16", "17"}; !slices.Equal(got, want) {
		t.Fatalf("PostgresVersions() = %v, want %v", got, want)
	}
	for _, version := range PostgresVersions() {
		image, pinned := Postgres(version)
		if !pinned {
			t.Fatalf("Postgres(%q) names no image, and PostgresVersions() lists it", version)
		}
		tag, digest, cut := strings.Cut(image, "@sha256:")
		if !cut || len(digest) != 64 {
			t.Errorf("Postgres(%q) = %q, which a registry can move under whoever pulls it", version, image)
		}
		if !strings.HasPrefix(tag, "postgres:"+version+".") {
			t.Errorf("Postgres(%q) = %q, which is another major's image", version, image)
		}
	}
}

func TestAPostgresVersionNothingPinsNamesNoImage(t *testing.T) {
	t.Parallel()

	if image, pinned := Postgres("9"); pinned {
		t.Errorf("Postgres(%q) = %q, and nothing pins that version", "9", image)
	}
}

func TestTheDefaultPostgresVersionIsOneThatIsPinned(t *testing.T) {
	t.Parallel()

	if _, pinned := Postgres(DefaultPostgresVersion); !pinned {
		t.Errorf("the default version %q names no pinned image", DefaultPostgresVersion)
	}
}
