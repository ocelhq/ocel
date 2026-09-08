package discovery

import (
	"path/filepath"
	"strings"
	"testing"
)

func rootDirs(t *testing.T, roots []Root, base string) []string {
	t.Helper()
	dirs := make([]string, len(roots))
	for i, r := range roots {
		rel, err := filepath.Rel(base, r.Dir)
		if err != nil {
			t.Fatalf("rel %q: %v", r.Dir, err)
		}
		dirs[i] = filepath.ToSlash(rel)
	}
	return dirs
}

func TestRoots(t *testing.T) {
	t.Run("defaults to the infra folder beside the config", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "infra", "main.ts"), "export {};")

		roots, err := Roots(root, nil, nil)
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != "infra" {
			t.Fatalf("roots = %v, want [infra]", got)
		}
		if roots[0].Language != JS {
			t.Errorf("Language = %q, want %q", roots[0].Language, JS)
		}
	})

	t.Run("defaults also to an infra folder inside every app", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "infra", "main.ts"), "export {};")
		write(t, filepath.Join(root, "apps", "web", "infra", "main.ts"), "export {};")

		roots, err := Roots(root, nil, []string{".", "apps/web"})
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 2 || got[0] != "infra" || got[1] != "apps/web/infra" {
			t.Fatalf("roots = %v, want [infra apps/web/infra]", got)
		}
	})

	t.Run("skips a default root that does not exist", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "apps", "web", "infra", "main.ts"), "export {};")

		roots, err := Roots(root, nil, []string{"apps/web"})
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != "apps/web/infra" {
			t.Fatalf("roots = %v, want [apps/web/infra]", got)
		}
	})

	t.Run("explicit paths replace the defaults", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "infra", "main.ts"), "export {};")
		write(t, filepath.Join(root, "packages", "one", "res", "main.ts"), "export {};")
		write(t, filepath.Join(root, "packages", "two", "res", "main.ts"), "export {};")

		roots, err := Roots(root, []string{"packages/*/res"}, []string{"."})
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		want := []string{"packages/one/res", "packages/two/res"}
		if got := rootDirs(t, roots, root); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("roots = %v, want %v", got, want)
		}
	})

	t.Run("an explicit path that does not exist is an error naming it", func(t *testing.T) {
		root := t.TempDir()

		_, err := Roots(root, []string{"declarations"}, nil)
		if err == nil {
			t.Fatal("Roots succeeded on a missing configured path, want an error")
		}
		if !strings.Contains(err.Error(), "declarations") {
			t.Errorf("err = %v, want it to name the configured path", err)
		}
	})

	t.Run("the nearest manifest names the language", func(t *testing.T) {
		for _, tc := range []struct {
			manifest string
			want     Language
		}{
			{"Cargo.toml", Rust},
			{"go.mod", Go},
			{"pyproject.toml", Python},
			{"requirements.txt", Python},
			{"package.json", JS},
		} {
			t.Run(tc.manifest, func(t *testing.T) {
				root := t.TempDir()
				write(t, filepath.Join(root, tc.manifest), "")
				write(t, filepath.Join(root, "infra", "keep"), "")

				roots, err := Roots(root, nil, nil)
				if err != nil {
					t.Fatalf("Roots: %v", err)
				}
				if len(roots) != 1 || roots[0].Language != tc.want {
					t.Fatalf("roots = %v, want one %s root", roots, tc.want)
				}
			})
		}
	})

	t.Run("a nested manifest wins over the one beside the config", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "package.json"), "{}")
		write(t, filepath.Join(root, "server", "go.mod"), "module example.com/web")
		write(t, filepath.Join(root, "server", "infra", "infra.go"), "package infra")

		roots, err := Roots(root, nil, []string{"server"})
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if len(roots) != 1 || roots[0].Language != Go {
			t.Fatalf("roots = %v, want one go root", roots)
		}
	})

	t.Run("a root is listed once however many apps reach it", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "infra", "main.ts"), "export {};")

		roots, err := Roots(root, nil, []string{".", "."})
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if len(roots) != 1 {
			t.Fatalf("roots = %v, want one", roots)
		}
	})
}
