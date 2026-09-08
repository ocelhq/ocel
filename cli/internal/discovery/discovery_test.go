package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type walkCase struct {
	name  string
	files map[string]string
	paths []string
	want  []string
}

func runWalkCases(t *testing.T, cases []walkCase, walk func(configDir string, paths []string) ([]string, error)) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			for path, contents := range tc.files {
				write(t, filepath.Join(root, filepath.FromSlash(path)), contents)
			}

			got, err := walk(root, tc.paths)
			if err != nil {
				t.Fatalf("walk: %v", err)
			}

			want := make([]string, 0, len(tc.want))
			for _, w := range tc.want {
				want = append(want, filepath.Join(root, filepath.FromSlash(w)))
			}
			assertFiles(t, got, want)
		})
	}
}

func TestDiscover(t *testing.T) {
	t.Parallel()

	runWalkCases(t, []walkCase{
		{
			name: "finds files under the default path",
			files: map[string]string{
				"infra/main.ts":       "export {};",
				"infra/sub/nested.ts": "export {};",
				"other.ts":            "export {};",
			},
			paths: []string{"infra"},
			want:  []string{"infra/main.ts", "infra/sub/nested.ts"},
		},
		{
			name: "ignores node_modules and hidden dirs",
			files: map[string]string{
				"infra/main.ts":             "export {};",
				"infra/node_modules/dep.ts": "export {};",
				"infra/.hidden/skip.ts":     "export {};",
			},
			paths: []string{"infra"},
			want:  []string{"infra/main.ts"},
		},
		{
			name: "filters non-source extensions",
			files: map[string]string{
				"infra/main.ts":   "export {};",
				"infra/README.md": "# not source",
			},
			paths: []string{"infra"},
			want:  []string{"infra/main.ts"},
		},
		{
			name: "supports glob patterns across packages",
			files: map[string]string{
				"packages/a/ocel/one.ts": "export {};",
				"packages/b/ocel/two.ts": "export {};",
			},
			paths: []string{"packages/*/ocel"},
			want:  []string{"packages/a/ocel/one.ts", "packages/b/ocel/two.ts"},
		},
		{
			name:  "a missing path yields no files and no error",
			paths: []string{"infra"},
		},
	}, Discover)
}

func TestDirs(t *testing.T) {
	t.Parallel()

	runWalkCases(t, []walkCase{
		{
			name: "returns the root and its subdirs, not files",
			files: map[string]string{
				"infra/main.ts":       "export {};",
				"infra/sub/nested.ts": "export {};",
			},
			paths: []string{"infra"},
			want:  []string{"infra", "infra/sub"},
		},
		{
			name: "ignores node_modules and hidden dirs",
			files: map[string]string{
				"infra/main.ts":             "export {};",
				"infra/node_modules/dep.ts": "export {};",
				"infra/.hidden/skip.ts":     "export {};",
			},
			paths: []string{"infra"},
			want:  []string{"infra"},
		},
		{
			name: "supports glob patterns across packages",
			files: map[string]string{
				"packages/a/ocel/one.ts": "export {};",
				"packages/b/ocel/two.ts": "export {};",
			},
			paths: []string{"packages/*/ocel"},
			want:  []string{"packages/a/ocel", "packages/b/ocel"},
		},
		{
			name:  "a missing path yields no dirs and no error",
			paths: []string{"infra"},
		},
	}, Dirs)
}

func assertFiles(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
