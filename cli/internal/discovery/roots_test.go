package discovery

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/constants"
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
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), "export {};")

		roots, err := Roots(root, nil)
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != constants.DefaultDiscoveryDirName {
			t.Fatalf("roots = %v, want [%s]", got, constants.DefaultDiscoveryDirName)
		}
		if roots[0].Language != language.JS {
			t.Errorf("Language = %q, want %q", roots[0].Language, language.JS)
		}
	})

	t.Run("an infra folder inside an app is not a default root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), "export {};")
		write(t, filepath.Join(root, "apps", "web", constants.DefaultDiscoveryDirName, "main.ts"), "export {};")

		roots, err := Roots(root, nil)
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != constants.DefaultDiscoveryDirName {
			t.Fatalf("roots = %v, want [%s]", got, constants.DefaultDiscoveryDirName)
		}
	})

	t.Run("the default root is skipped when it does not exist", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "apps", "web", constants.DefaultDiscoveryDirName, "main.ts"), "export {};")

		roots, err := Roots(root, nil)
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if len(roots) != 0 {
			t.Fatalf("roots = %v, want none", rootDirs(t, roots, root))
		}
	})

	t.Run("explicit paths replace the defaults", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), "export {};")
		write(t, filepath.Join(root, "packages", "one", "res", "main.ts"), "export {};")
		write(t, filepath.Join(root, "packages", "two", "res", "main.ts"), "export {};")

		roots, err := Roots(root, []string{"packages/*/res"})
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

		_, err := Roots(root, []string{"declarations"})
		if err == nil {
			t.Fatal("Roots succeeded on a missing configured path, want an error")
		}
		if !strings.Contains(err.Error(), "declarations") {
			t.Errorf("err = %v, want it to name the configured path", err)
		}
	})

	t.Run("the source files in a folder name its language", func(t *testing.T) {
		for _, tc := range []struct {
			file string
			want language.Language
		}{
			{"main.ts", language.JS},
			{"infra.go", language.Go},
			{"infra.py", language.Python},
		} {
			t.Run(tc.file, func(t *testing.T) {
				root := t.TempDir()
				write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "nested", tc.file), "")
				write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "README.md"), "")

				roots, err := Roots(root, nil)
				if err != nil {
					t.Fatalf("Roots: %v", err)
				}
				if len(roots) != 1 || roots[0].Language != tc.want {
					t.Fatalf("roots = %v, want one %s root", roots, tc.want)
				}
			})
		}
	})

	t.Run("a folder of rust files is an error sending the declarations to the crate", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "mod.rs"), "pub const NAME: &str = \"main\";")

		_, err := Roots(root, nil)
		if err == nil {
			t.Fatal("Roots succeeded on a folder of rust files, want an error")
		}
		for _, want := range []string{filepath.Join(root, constants.DefaultDiscoveryDirName), "crate"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a manifest beside the config does not name the folder's language", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "package.json"), "{}")
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "infra.go"), "package "+constants.DefaultDiscoveryDirName)

		roots, err := Roots(root, nil)
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if len(roots) != 1 || roots[0].Language != language.Go {
			t.Fatalf("roots = %v, want one go root", roots)
		}
	})

	t.Run("a folder of two languages is an error naming both", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "infra.go"), "package "+constants.DefaultDiscoveryDirName)
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), "export {};")

		_, err := Roots(root, nil)
		if err == nil {
			t.Fatal("Roots succeeded on a folder of two languages, want an error")
		}
		for _, want := range []string{"mixes", string(language.Go), string(language.JS)} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("a folder with no source files is skipped", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "README.md"), "")
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "node_modules", "dep", "index.js"), "")

		roots, err := Roots(root, nil)
		if err != nil {
			t.Fatalf("Roots: %v", err)
		}
		if len(roots) != 0 {
			t.Fatalf("roots = %v, want none", rootDirs(t, roots, root))
		}
	})
}

const (
	rustOcelManifest      = rustBinManifest + "\n[dependencies]\nocel-sdk = { path = \"../../ocel-sdk\" }\n"
	rustWorkspaceManifest = rustBinManifest + "\n[dependencies]\nocel-sdk.workspace = true\n"
	rustTargetManifest    = rustBinManifest + "\n[target.'cfg(not(target_arch = \"wasm32\"))'.dependencies]\nocel-sdk = { path = \"../../ocel-sdk\" }\n"
	rustDevOnlyManifest   = rustBinManifest + "\n[dev-dependencies]\nocel-sdk = { path = \"../../ocel-sdk\" }\n\n[build-dependencies]\nocel-sdk = { path = \"../../ocel-sdk\" }\n"
	rustRenamedManifest   = rustBinManifest + "\n[dependencies]\nocel = { package = \"ocel-sdk\", version = \"0.0.1\" }\n"
	rustOtherOcelManifest = rustBinManifest + "\n[dependencies]\nocel = \"0.1\"\n"
)

func TestRootsOfAddsTheCratesAProjectDeclaresFrom(t *testing.T) {
	t.Run("a crate at the config dir and a crate under an app path are both roots", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "Cargo.toml"), rustOcelManifest)
		write(t, filepath.Join(root, "apps", "api", "Cargo.toml"), rustWorkspaceManifest)
		write(t, filepath.Join(root, "apps", "web", "package.json"), "{}")

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{
			{Name: "api", Path: "apps/api"},
			{Name: "web", Path: "apps/web"},
		}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		want := []string{".", "apps/api"}
		if got := rootDirs(t, roots, root); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("roots = %v, want %v", got, want)
		}
		for _, r := range roots {
			if r.Language != language.Rust {
				t.Errorf("Language = %q, want %q", r.Language, language.Rust)
			}
		}
	})

	t.Run("a crate that does not depend on ocel is no root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "Cargo.toml"), rustBinManifest)
		write(t, filepath.Join(root, "apps", "web", "Cargo.toml"), rustBinManifest)

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if len(roots) != 0 {
			t.Fatalf("roots = %v, want none: nothing here declares through ocel", rootDirs(t, roots, root))
		}
	})

	t.Run("a crate depending on ocel-sdk under another name is a root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "apps", "api", "Cargo.toml"), rustRenamedManifest)

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != "apps/api" {
			t.Fatalf("roots = %v, want [apps/api]", got)
		}
	})

	t.Run("a crate depending on the unrelated crates.io ocel is no root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "apps", "api", "Cargo.toml"), rustOtherOcelManifest)

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if len(roots) != 0 {
			t.Fatalf("roots = %v, want none: the crate named ocel on crates.io is not the sdk", rootDirs(t, roots, root))
		}
	})

	t.Run("a crate depending on ocel only under a target table is a root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "apps", "api", "Cargo.toml"), rustTargetManifest)

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != "apps/api" {
			t.Fatalf("roots = %v, want [apps/api]", got)
		}
	})

	t.Run("a crate depending on ocel only to build or to test is no root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "apps", "api", "Cargo.toml"), rustDevOnlyManifest)

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "api", Path: "apps/api"}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if len(roots) != 0 {
			t.Fatalf("roots = %v, want none: neither dev- nor build-dependencies link into the bin", rootDirs(t, roots, root))
		}
	})

	t.Run("an app crate that is the config dir is named once", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "Cargo.toml"), rustOcelManifest)
		write(t, filepath.Join(root, "package.json"), "{}")
		write(t, filepath.Join(root, "ocel.config.ts"), "export default {};")

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "."}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != "." {
			t.Fatalf("roots = %v, want [.]", got)
		}
	})

	t.Run("a project with no crate gets no rust root", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), "export {};")
		write(t, filepath.Join(root, "apps", "web", "package.json"), "{}")

		roots, err := RootsOf(&projectconfig.Config{Dir: root, Apps: []projectconfig.App{{Name: "web", Path: "apps/web"}}})
		if err != nil {
			t.Fatalf("RootsOf: %v", err)
		}
		if got := rootDirs(t, roots, root); len(got) != 1 || got[0] != constants.DefaultDiscoveryDirName {
			t.Fatalf("roots = %v, want [%s]", got, constants.DefaultDiscoveryDirName)
		}
	})
}
