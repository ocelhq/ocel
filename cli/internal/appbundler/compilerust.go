package appbundler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/cargo"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const cargoManifestFile = "Cargo.toml"

const rustLinker = "rust-lld"

func (c Compilation) compileRust(ctx context.Context) error {
	manifest, err := os.Stat(filepath.Join(c.Source, cargoManifestFile))
	if err != nil || !manifest.Mode().IsRegular() {
		return fmt.Errorf("app %q is built with rust and %s holds no %s: an app is compiled from the crate rooted in its own directory", c.App, c.Source, cargoManifestFile)
	}
	target, runs := providerkit.RustTarget(c.Framework.Arch)
	if !runs {
		return fmt.Errorf("app %q asks to be compiled for %q, which names no architecture rust builds for", c.App, c.Framework.Arch)
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		return fmt.Errorf("app %q is built with rust and no cargo is on PATH: %w", c.App, err)
	}
	source, err := filepath.Abs(c.Source)
	if err != nil {
		return err
	}
	workspace, err := cargo.Metadata(ctx, source, "--no-deps")
	if err != nil {
		return fmt.Errorf("app %q: %w", c.App, err)
	}
	crate, ok := workspace.PackageAt(source)
	if !ok {
		return fmt.Errorf("app %q is built with rust and the %s in %s names no package to build", c.App, cargoManifestFile, source)
	}
	bins := crate.Bins()
	if len(bins) != 1 {
		return fmt.Errorf("app %q is built from the crate %s at %s, which builds %d binaries, and a function execs exactly one: keep one bin target in the crate", c.App, crate.Name, source, len(bins))
	}

	cmd := exec.CommandContext(ctx, "cargo", "build", "--release",
		"--manifest-path", crate.ManifestPath, "--bin", bins[0].Name, "--target", target)
	cmd.Dir = workspace.Root
	cmd.Env = append(os.Environ(), "CARGO_PROFILE_RELEASE_STRIP=symbols")
	linker := "CARGO_TARGET_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_LINKER"
	if _, set := os.LookupEnv(linker); !set && !cargoConfigNamesLinker(workspace.Root, target) {
		cmd.Env = append(cmd.Env, linker+"="+rustLinker)
	}
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("compile app %q in %s for %s (%w):\n%s", c.App, source, target, err, said.String())
	}
	c.report("compiling", said.String())

	if err := os.MkdirAll(c.FuncDir, 0o755); err != nil {
		return err
	}
	built := filepath.Join(workspace.Target, target, "release", bins[0].Name)
	if err := copyFile(built, filepath.Join(c.FuncDir, c.App), 0o755); err != nil {
		return fmt.Errorf("app %q: cargo reported a build and left no binary at %s: %w", c.App, built, err)
	}
	return describeArtifact(c.App, c.Framework, c.App, []string{"./" + c.App}, c.FuncDir, c.AppDir)
}

func cargoConfigNamesLinker(workspaceRoot, target string) bool {
	var configs []string
	for dir := workspaceRoot; ; dir = filepath.Dir(dir) {
		configs = append(configs, filepath.Join(dir, ".cargo", "config.toml"), filepath.Join(dir, ".cargo", "config"))
		if filepath.Dir(dir) == dir {
			break
		}
	}
	home := os.Getenv("CARGO_HOME")
	if home == "" {
		if user, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(user, ".cargo")
		}
	}
	if home != "" {
		configs = append(configs, filepath.Join(home, "config.toml"), filepath.Join(home, "config"))
	}
	for _, path := range configs {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config struct {
			Target map[string]struct {
				Linker string `toml:"linker"`
			} `toml:"target"`
		}
		if toml.Unmarshal(body, &config) == nil && config.Target[target].Linker != "" {
			return true
		}
	}
	return false
}
