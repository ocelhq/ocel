package appbundler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
)

const (
	npmCommand    = "npm"
	npmConfigFile = ".npmrc"
)

type resolvingForSplit struct{}

type manifest struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	OS                   []string          `json:"os"`
	CPU                  []string          `json:"cpu"`
	Libc                 []string          `json:"libc"`
}

type platformPackages struct {
	arch string

	mu       sync.Mutex
	split    map[string]*manifest
	wanted   map[string][]string
	imported map[string]string
}

func (p *platformPackages) plugin() api.Plugin {
	return api.Plugin{
		Name: "ocel-platform-package",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^[^./]`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if _, again := args.PluginData.(resolvingForSplit); again || args.Kind == api.ResolveEntryPoint {
					return api.OnResolveResult{}, nil
				}
				resolved := build.Resolve(args.Path, api.ResolveOptions{
					Importer:   args.Importer,
					ResolveDir: args.ResolveDir,
					Kind:       args.Kind,
					PluginData: resolvingForSplit{},
				})
				if len(resolved.Errors) > 0 || resolved.External || !filepath.IsAbs(resolved.Path) {
					return api.OnResolveResult{}, nil
				}
				root, _, inPackage := packageRoot(filepath.Dir(resolved.Path))
				if !inPackage || !p.splitsByPlatform(root, specifierPackage(args.Path)) {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: args.Path, External: true}, nil
			})
		},
	}
}

func (p *platformPackages) splitsByPlatform(root, imported string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.split == nil {
		p.split = map[string]*manifest{}
		p.wanted = map[string][]string{}
		p.imported = map[string]string{}
	}
	pkg, seen := p.split[root]
	if !seen {
		pkg = platformSplit(root)
		p.split[root] = pkg
	}
	if pkg == nil {
		return false
	}
	spec := pkg.Version
	if imported != pkg.Name {
		spec = "npm:" + pkg.Name + "@" + pkg.Version
	}
	p.imported[imported] = root
	if !slices.Contains(p.wanted[imported], spec) {
		p.wanted[imported] = append(p.wanted[imported], spec)
	}
	return true
}

func platformSplit(root string) *manifest {
	pkg, err := readManifest(root)
	if err != nil || pkg.Version == "" {
		return nil
	}
	for dep := range pkg.OptionalDependencies {
		if platformVariantName.MatchString(dep) {
			return &pkg
		}
		if installed, _, found := installedManifest(root, dep); found && installed.platformVariant() {
			return &pkg
		}
	}
	return nil
}

func specifierPackage(specifier string) string {
	parts := strings.SplitN(specifier, "/", 3)
	if strings.HasPrefix(specifier, "@") && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

var platformVariantName = regexp.MustCompile(`(?:^|[/-])(?:aix|android|darwin|freebsd|linux|openbsd|sunos|win32)-(?:arm|arm64|ia32|loong64|mips64el|ppc64|riscv64|s390x|universal|x64)(?:-|$)`)

func readManifest(dir string) (manifest, error) {
	var pkg manifest
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return pkg, err
	}
	return pkg, json.Unmarshal(raw, &pkg)
}

func installedManifest(from, name string) (manifest, string, bool) {
	for dir := from; ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) != nodeModulesDirName {
			root := filepath.Join(dir, nodeModulesDirName, filepath.FromSlash(name))
			if pkg, err := readManifest(root); err == nil {
				return pkg, root, true
			}
		}
		if filepath.Dir(dir) == dir {
			return manifest{}, "", false
		}
	}
}

func (m manifest) platformVariant() bool {
	return len(m.OS) > 0 || len(m.CPU) > 0
}

func (p *platformPackages) pins() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	pins := map[string]any{}
	for imported, root := range p.imported {
		pkg := p.split[root]
		pinned := installedGraph(root, *pkg, []string{root})
		if len(pinned) == 0 {
			continue
		}
		if imported == pkg.Name {
			imported += "@" + pkg.Version
		}
		pins[imported] = pinned
	}
	return pins
}

func installedGraph(root string, pkg manifest, path []string) map[string]any {
	pinned := map[string]any{}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.OptionalDependencies} {
		for dep := range deps {
			child, childRoot, found := installedManifest(root, dep)
			if !found || child.platformVariant() || slices.Contains(path, childRoot) {
				continue
			}
			version := child.Version
			if child.Name != dep {
				version = "npm:" + child.Name + "@" + child.Version
			}
			below := installedGraph(childRoot, child, append(slices.Clone(path), childRoot))
			if len(below) == 0 {
				pinned[dep] = version
				continue
			}
			below["."] = version
			pinned[dep] = below
		}
	}
	return pinned
}

func (p *platformPackages) installInto(ctx context.Context, app, source, funcDir string) error {
	p.mu.Lock()
	reached := p.wanted
	p.mu.Unlock()
	if len(reached) == 0 {
		return nil
	}
	wanted := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(reached)) {
		versions := reached[name]
		if len(versions) > 1 {
			slices.Sort(versions)
			return fmt.Errorf("app %q reaches %s at versions %s, and it ships one package per platform: a function contains one copy of it, installed for its architecture, so every importer must agree on the version",
				app, name, strings.Join(versions, ", "))
		}
		wanted[name] = versions[0]
	}
	names := strings.Join(slices.Sorted(maps.Keys(wanted)), ", ")
	cpu, known := arch.NodePackageCPU(p.arch)
	if !known {
		return fmt.Errorf("app %q depends on %s, which ship one package per platform, and declares architecture %q, which has no npm cpu", app, names, p.arch)
	}
	if _, err := exec.LookPath(npmCommand); err != nil {
		return fmt.Errorf("app %q depends on %s, which ship one package per platform, and no %s is on PATH to install them for %s/%s: %w",
			app, names, npmCommand, arch.NodePackageOS, cpu, err)
	}

	staging, err := os.MkdirTemp("", "ocel-platform-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := writeJSON(filepath.Join(staging, "package.json"), map[string]any{"private": true, "dependencies": wanted, "overrides": p.pins()}); err != nil {
		return err
	}
	if config, found := projectNpmConfig(source); found {
		if err := copyFile(config, filepath.Join(staging, npmConfigFile), 0o600); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, npmCommand, npmInstallArgs(cpu)...)
	cmd.Dir = staging
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install %s for app %q on %s/%s (%w):\n%s", names, app, arch.NodePackageOS, cpu, err, said.String())
	}
	for _, name := range slices.Sorted(maps.Keys(wanted)) {
		if !installedForTarget(filepath.Join(staging, nodeModulesDirName), name, cpu) {
			return fmt.Errorf("npm installed %s for app %q without a package built for %s/%s/%s, so the function would fail at its first require of it; npm reported:\n%s",
				name, app, arch.NodePackageOS, cpu, arch.NodePackageLibc, said.String())
		}
	}
	staged := filepath.Join(staging, nodeModulesDirName)
	entries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if slices.Contains(npmBookkeeping, entry.Name()) {
			continue
		}
		if err := copyTree(filepath.Join(staged, entry.Name()), filepath.Join(funcDir, nodeModulesDirName, entry.Name()), func(string) bool { return false }); err != nil {
			return err
		}
	}
	return nil
}

var npmBookkeeping = []string{".bin", ".package-lock.json"}

func installedForTarget(nodeModules, name, cpu string) bool {
	root := filepath.Join(nodeModules, filepath.FromSlash(name))
	pkg, err := readManifest(root)
	if err != nil {
		return false
	}
	expected := false
	for dep := range pkg.OptionalDependencies {
		expected = expected || namesVariantFor(dep, cpu)
		for _, dir := range []string{filepath.Join(root, nodeModulesDirName), nodeModules} {
			variant, err := readManifest(filepath.Join(dir, filepath.FromSlash(dep)))
			if err == nil && variant.platformVariant() && variant.runsOn(cpu) {
				return true
			}
		}
	}
	return !expected
}

func namesVariantFor(dep, cpu string) bool {
	return regexp.MustCompile(`(?:^|[/-])` + arch.NodePackageOS + `-` + regexp.QuoteMeta(cpu) + `(?:-|$)`).MatchString(dep)
}

func (m manifest) runsOn(cpu string) bool {
	return allows(m.OS, arch.NodePackageOS) && allows(m.CPU, cpu) && allows(m.Libc, arch.NodePackageLibc)
}

func allows(declared []string, value string) bool {
	if len(declared) == 0 || slices.Contains(declared, value) {
		return true
	}
	if slices.Contains(declared, "!"+value) {
		return false
	}
	return !slices.ContainsFunc(declared, func(entry string) bool { return !strings.HasPrefix(entry, "!") })
}

func projectNpmConfig(from string) (string, bool) {
	for dir := from; ; dir = filepath.Dir(dir) {
		config := filepath.Join(dir, npmConfigFile)
		if info, err := os.Stat(config); err == nil && info.Mode().IsRegular() {
			return config, true
		}
		if hasAny(dir, ".git", "pnpm-workspace.yaml") || filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

func hasAny(dir string, names ...string) bool {
	return slices.ContainsFunc(names, func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	})
}

func npmInstallArgs(cpu string) []string {
	return []string{
		"install",
		"--os=" + arch.NodePackageOS,
		"--cpu=" + cpu,
		"--libc=" + arch.NodePackageLibc,
		"--ignore-scripts",
		"--no-bin-links",
		"--no-package-lock",
		"--no-audit",
		"--no-fund",
	}
}
