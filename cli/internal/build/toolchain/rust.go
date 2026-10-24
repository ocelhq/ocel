package toolchain

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/cargo"
	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/pkg/arch"
)

const cargoManifestFile = "Cargo.toml"

const rustLinker = "rust-lld"

func (c Compilation) compileRust(ctx context.Context) error {
	manifest, err := os.Stat(filepath.Join(c.Source, cargoManifestFile))
	if err != nil || !manifest.Mode().IsRegular() {
		return fmt.Errorf("app %q is built with rust and %s has no %s: an app is compiled from the crate rooted in its own directory", c.App, c.Source, cargoManifestFile)
	}
	target, runs := arch.RustTarget(c.Framework.Arch)
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
	bin, err := crate.FindBin(c.Entrypoint)
	if err != nil {
		return fmt.Errorf("app %q is built from the crate at %s, and a function execs one binary: %w", c.App, source, err)
	}

	cmd := exec.Command("cargo", "build", "--release",
		"--manifest-path", crate.ManifestPath, "--bin", bin.Name, "--target", target)
	cmd.Dir = workspace.Root
	cmd.Env = c.environ("CARGO_PROFILE_RELEASE_STRIP=symbols")
	config := readCargoConfig(workspace.Root, target)
	linkerVariable := "CARGO_TARGET_" + strings.ToUpper(strings.ReplaceAll(target, "-", "_")) + "_LINKER"
	linker, set := lookupEnviron(cmd.Env, linkerVariable)
	if !set {
		linker = config.linker
	}
	if !set && linker == "" {
		linker = rustLinker
		cmd.Env = append(cmd.Env, linkerVariable+"="+rustLinker)
	}
	if err := c.refuseCWithNoCompiler(ctx, workspace.Root, crate, target, linker, config, cmd.Env); err != nil {
		return err
	}
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := childprocess.Run(ctx, cmd); err != nil {
		return fmt.Errorf("compile app %q in %s for %s (%w):\n%s", c.App, source, target, err, said.String())
	}
	c.report("compiling", said.String())

	if err := os.MkdirAll(c.FunctionDir, 0o755); err != nil {
		return err
	}
	built := filepath.Join(workspace.Target, target, "release", bin.Name)
	if err := copyFile(built, filepath.Join(c.FunctionDir, c.App), 0o755); err != nil {
		return fmt.Errorf("app %q: cargo reported a build and left no binary at %s: %w", c.App, built, err)
	}
	return describeArtifact(c.App, c.Framework, c.App, []string{"./" + c.App}, []string{"./" + c.App}, c.FunctionDir, c.AppDir)
}

func (c Compilation) refuseCWithNoCompiler(ctx context.Context, workspaceRoot string, crate cargo.Package, target, linker string, config cargoConfig, environ []string) error {
	compilers := listMuslCCompilers(target)
	if namesCCompiler(target, linker, config, environ, compilers) || isRustHost(ctx, workspaceRoot, target, environ) {
		return nil
	}
	crates, err := cargo.ListPackagesBuildingC(ctx, workspaceRoot, crate.ManifestPath, target, environ)
	if err != nil || len(crates) == 0 {
		return nil
	}
	return fmt.Errorf("app %q compiles C for %s in %s %s, and no C compiler for that target is on PATH: install %s, or set CC_%s to one",
		c.App, target, english.Number(len(crates), "the crate", "the crates"), english.And(crates), english.Or(compilers), strings.ReplaceAll(target, "-", "_"))
}

func listMuslCCompilers(target string) []string {
	architecture, _, _ := strings.Cut(target, "-")
	compilers := []string{architecture + "-linux-musl-gcc"}
	if architecture == "x86_64" {
		compilers = append(compilers, "musl-gcc")
	}
	return compilers
}

func namesCCompiler(target, linker string, config cargoConfig, environ, compilers []string) bool {
	for _, name := range []string{"CC_" + target, "CC_" + strings.ReplaceAll(target, "-", "_"), "TARGET_CC", "CC", "CROSS_COMPILE"} {
		if value, _ := lookupEnviron(environ, name); strings.TrimSpace(value) != "" || config.env[name] {
			return true
		}
	}
	if strings.HasSuffix(linker, "-gcc") {
		return true
	}
	path, _ := lookupEnviron(environ, "PATH")
	for _, dir := range filepath.SplitList(path) {
		for _, compiler := range compilers {
			if _, err := os.Stat(filepath.Join(dir, compiler)); err == nil {
				return true
			}
		}
	}
	return false
}

func isRustHost(ctx context.Context, workspaceRoot, target string, environ []string) bool {
	rustc, set := lookupEnviron(environ, "RUSTC")
	if !set || rustc == "" {
		rustc = "rustc"
	}
	cmd := exec.Command(rustc, "-vV")
	cmd.Dir = workspaceRoot
	cmd.Env = environ
	var said bytes.Buffer
	cmd.Stdout = &said
	if childprocess.Run(ctx, cmd) != nil {
		return false
	}
	return slices.Contains(strings.Split(said.String(), "\n"), "host: "+target)
}

func lookupEnviron(environ []string, name string) (string, bool) {
	for _, entry := range slices.Backward(environ) {
		if key, value, _ := strings.Cut(entry, "="); key == name {
			return value, true
		}
	}
	return "", false
}

type cargoConfig struct {
	linker string
	env    map[string]bool
}

func readCargoConfig(workspaceRoot, target string) cargoConfig {
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
	read := cargoConfig{env: map[string]bool{}}
	for _, path := range configs {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config struct {
			Target map[string]struct {
				Linker string `toml:"linker"`
			} `toml:"target"`
			Env map[string]any `toml:"env"`
		}
		if toml.Unmarshal(body, &config) != nil {
			continue
		}
		if read.linker == "" {
			read.linker = config.Target[target].Linker
		}
		for name := range config.Env {
			read.env[name] = true
		}
	}
	return read
}
