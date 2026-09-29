package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactHash(t *testing.T) {
	t.Parallel()

	hashOf := func(t *testing.T, files tree, prepare func(t *testing.T, root string)) string {
		t.Helper()
		root := t.TempDir()
		writeTree(t, root, files)
		if prepare != nil {
			prepare(t, root)
		}
		sum, err := artifactHash(root)
		if err != nil {
			t.Fatalf("artifactHash: %v", err)
		}
		return sum
	}

	base := tree{"index.mjs": "one", "nested/dep.js": "two"}

	t.Run("is 16 lowercase hex characters", func(t *testing.T) {
		t.Parallel()

		sum := hashOf(t, base, nil)
		if len(sum) != buildIDLength {
			t.Fatalf("hash = %q, want %d characters", sum, buildIDLength)
		}
		if sum != strings.ToLower(sum) {
			t.Errorf("hash = %q, want lowercase", sum)
		}
		if strings.Trim(sum, "0123456789abcdef") != "" {
			t.Errorf("hash = %q, want hex", sum)
		}
	})

	t.Run("an empty directory still hashes", func(t *testing.T) {
		t.Parallel()

		if sum := hashOf(t, tree{}, nil); len(sum) != buildIDLength {
			t.Errorf("hash = %q, want %d characters", sum, buildIDLength)
		}
	})

	variants := []struct {
		name  string
		files tree
		same  bool
	}{
		{name: "the same tree", files: tree{"index.mjs": "one", "nested/dep.js": "two"}, same: true},
		{name: "different contents", files: tree{"index.mjs": "ONE", "nested/dep.js": "two"}},
		{name: "a different path", files: tree{"index.mjs": "one", "nested/other.js": "two"}},
		{name: "an extra file", files: tree{"index.mjs": "one", "nested/dep.js": "two", "extra.js": ""}},
		{name: "contents swapped between paths", files: tree{"index.mjs": "two", "nested/dep.js": "one"}},
	}
	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			want := hashOf(t, base, nil)
			got := hashOf(t, tt.files, nil)
			if tt.same && got != want {
				t.Errorf("hash = %q, want %q", got, want)
			}
			if !tt.same && got == want {
				t.Errorf("hash = %q for %s, want it to differ", got, tt.name)
			}
		})
	}

	t.Run("directories and symlinks do not count", func(t *testing.T) {
		t.Parallel()

		want := hashOf(t, base, nil)
		got := hashOf(t, base, func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "empty", "deeper"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "index.mjs"), filepath.Join(root, "link.mjs")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		})
		if got != want {
			t.Errorf("hash = %q, want %q — only regular files count", got, want)
		}
	})
}
