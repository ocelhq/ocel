package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/arch"
)

func TestANativeAddonMatchesTheArchitectureTheAppDeclares(t *testing.T) {
	t.Parallel()

	const addonPath = "node_modules/native-dep/build/Release/addon.node"
	treeWith := func(addon string) tree {
		return tree{
			"package.json":                         appPkg,
			"server.js":                            "import native from 'native-dep';\nconsole.log(native);\n",
			"node_modules/native-dep/package.json": `{"name":"native-dep","main":"index.js"}`,
			"node_modules/native-dep/index.js":     "module.exports = require('./build/Release/addon.node');\n",
			addonPath:                              addon,
		}
	}
	on := func(l layout, architecture string) Target {
		target := l.target("server.js")
		target.Framework.Arch = architecture
		return target
	}

	t.Run("an addon built for the declared architecture is placed", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, treeWith(elfAddon(arch.ARM64, "aarch64")))
		if err := Bundle(context.Background(), on(l, arch.ARM64)); err != nil {
			t.Fatalf("Bundle: %v", err)
		}
		if got := readFile(t, filepath.Join(l.functionDir, filepath.FromSlash(addonPath))); got != elfAddon(arch.ARM64, "aarch64") {
			t.Errorf("copied addon = %q, want the original bytes", got)
		}
	})

	t.Run("an addon built for another architecture fails the build", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, treeWith(elfAddon(arch.X8664, "amd64")))
		err := Bundle(context.Background(), on(l, arch.ARM64))
		if err == nil {
			t.Fatal("Bundle succeeded, want a refusal rather than a function that dies at its first require")
		}
		for _, want := range []string{"addon.node", arch.X8664, arch.ARM64, `"compute": "container"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("a package shipping one prebuild per platform keeps only the declared one", func(t *testing.T) {
		t.Parallel()

		prebuilt := tree{
			"package.json":                        appPkg,
			"server.js":                           "import native from 'multi-dep';\nconsole.log(native);\n",
			"node_modules/multi-dep/package.json": `{"name":"multi-dep","main":"index.js"}`,
			"node_modules/multi-dep/index.js": "module.exports = process.arch === 'arm64'\n" +
				"  ? require('./prebuilds/linux-arm64.node')\n" +
				"  : process.platform === 'darwin'\n" +
				"    ? require('./prebuilds/darwin-arm64.node')\n" +
				"    : require('./prebuilds/linux-x64.node');\n",
			"node_modules/multi-dep/prebuilds/linux-x64.node":    elfAddon(arch.X8664, "amd64"),
			"node_modules/multi-dep/prebuilds/linux-arm64.node":  elfAddon(arch.ARM64, "aarch64"),
			"node_modules/multi-dep/prebuilds/darwin-arm64.node": "\xcf\xfa\xed\xfe" + strings.Repeat("\x00", 28),
		}
		kept := map[string]string{
			arch.X8664: "node_modules/multi-dep/prebuilds/linux-x64.node",
			arch.ARM64: "node_modules/multi-dep/prebuilds/linux-arm64.node",
		}
		for _, architecture := range []string{arch.X8664, arch.ARM64} {
			t.Run(architecture, func(t *testing.T) {
				t.Parallel()

				l := newLayout(t, prebuilt)
				if err := Bundle(context.Background(), on(l, architecture)); err != nil {
					t.Fatalf("Bundle: %v", err)
				}
				for name, rel := range kept {
					dest := filepath.Join(l.functionDir, filepath.FromSlash(rel))
					_, err := os.Stat(dest)
					if name == architecture && err != nil {
						t.Errorf("%s is not in the bundle: %v", rel, err)
					}
					if name != architecture && err == nil {
						t.Errorf("%s is in the bundle, want only what %s loads", rel, architecture)
					}
				}
				if _, err := os.Stat(filepath.Join(l.functionDir, filepath.FromSlash(
					"node_modules/multi-dep/prebuilds/darwin-arm64.node"))); err == nil {
					t.Error("a mach-o prebuild is in the bundle, want only what linux loads")
				}
			})
		}
	})

	t.Run("a package with nothing loadable on the declared architecture fails the build", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, tree{
			"package.json":                        appPkg,
			"server.js":                           "import native from 'multi-dep';\nconsole.log(native);\n",
			"node_modules/multi-dep/package.json": `{"name":"multi-dep","main":"index.js"}`,
			"node_modules/multi-dep/index.js": "module.exports = process.platform === 'darwin'\n" +
				"  ? require('./prebuilds/darwin-arm64.node')\n" +
				"  : require('./prebuilds/linux-arm64.node');\n",
			"node_modules/multi-dep/prebuilds/linux-arm64.node":  elfAddon(arch.ARM64, "aarch64"),
			"node_modules/multi-dep/prebuilds/darwin-arm64.node": "\xcf\xfa\xed\xfe" + strings.Repeat("\x00", 28),
		})
		err := Bundle(context.Background(), on(l, arch.X8664))
		if err == nil {
			t.Fatal("Bundle succeeded, want a refusal rather than a function that dies at its first require")
		}
		for _, want := range []string{"linux-arm64.node", "darwin-arm64.node", arch.ARM64, arch.X8664, "linux ELF"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("an addon that is not a linux binary fails the build", func(t *testing.T) {
		t.Parallel()

		l := newLayout(t, treeWith("\xcf\xfa\xed\xfe"+strings.Repeat("\x00", 28)))
		err := Bundle(context.Background(), on(l, arch.X8664))
		if err == nil {
			t.Fatal("Bundle succeeded, want a mach-o addon refused as not linux")
		}
		for _, want := range []string{"addon.node", "linux"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err, want)
			}
		}
	})
}
