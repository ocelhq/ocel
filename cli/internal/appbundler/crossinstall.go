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
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const npmCommand = "npm"

type resolvingForSplit struct{}

type manifest struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	OS                   []string          `json:"os"`
	CPU                  []string          `json:"cpu"`
	Libc                 []string          `json:"libc"`
}

type platformPackages struct {
	arch string

	mu     sync.Mutex
	split  map[string]bool
	wanted map[string][]string
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
				if !inPackage || !p.splitsByPlatform(root) {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: args.Path, External: true}, nil
			})
		},
	}
}

func (p *platformPackages) splitsByPlatform(root string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if split, seen := p.split[root]; seen {
		return split
	}
	pkg, err := readManifest(root)
	split := err == nil && pkg.Version != "" && slices.ContainsFunc(sortedKeys(pkg.OptionalDependencies), func(dep string) bool {
		installed, found := installedManifest(root, dep)
		return found && (len(installed.OS) > 0 || len(installed.CPU) > 0)
	})
	if p.split == nil {
		p.split = map[string]bool{}
		p.wanted = map[string][]string{}
	}
	p.split[root] = split
	if split && !slices.Contains(p.wanted[pkg.Name], pkg.Version) {
		p.wanted[pkg.Name] = append(p.wanted[pkg.Name], pkg.Version)
	}
	return split
}

func readManifest(dir string) (manifest, error) {
	var pkg manifest
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return pkg, err
	}
	return pkg, json.Unmarshal(raw, &pkg)
}

func installedManifest(from, name string) (manifest, bool) {
	for dir := from; ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) != nodeModulesDirName {
			if pkg, err := readManifest(filepath.Join(dir, nodeModulesDirName, filepath.FromSlash(name))); err == nil {
				return pkg, true
			}
		}
		if filepath.Dir(dir) == dir {
			return manifest{}, false
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (p *platformPackages) installInto(ctx context.Context, app, funcDir string, log func(string)) error {
	p.mu.Lock()
	reached := p.wanted
	p.mu.Unlock()
	if len(reached) == 0 {
		return nil
	}
	wanted := map[string]string{}
	for _, name := range sortedKeys(reached) {
		versions := reached[name]
		if len(versions) > 1 {
			slices.Sort(versions)
			return fmt.Errorf("app %q reaches %s at versions %s, and it ships one package per platform: a function holds one copy of it, installed for its architecture, so every importer must agree on the version",
				app, name, strings.Join(versions, ", "))
		}
		wanted[name] = versions[0]
	}
	names := strings.Join(sortedKeys(wanted), ", ")
	cpu, known := providerkit.NodePackageCPU(p.arch)
	if !known {
		return fmt.Errorf("app %q depends on %s, which ship one package per platform, and declares architecture %q, which nothing runs it on", app, names, p.arch)
	}
	if _, err := exec.LookPath(npmCommand); err != nil {
		return fmt.Errorf("app %q depends on %s, which ship one package per platform, and no %s is on PATH to install them for %s/%s: %w",
			app, names, npmCommand, providerkit.NodePackageOS, cpu, err)
	}

	staging, err := os.MkdirTemp("", "ocel-platform-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := writeJSON(filepath.Join(staging, "package.json"), map[string]any{"private": true, "dependencies": wanted}); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, npmCommand, npmInstallArgs(cpu)...)
	cmd.Dir = staging
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install %s for app %q on %s/%s (%w):\n%s", names, app, providerkit.NodePackageOS, cpu, err, said.String())
	}
	log(said.String())
	for _, name := range slices.Sorted(maps.Keys(wanted)) {
		if !installedForTarget(filepath.Join(staging, nodeModulesDirName), name, cpu) {
			return fmt.Errorf("npm installed %s for app %q without a package built for %s/%s/%s, so the function would fail at its first require of it; npm reported:\n%s",
				name, app, providerkit.NodePackageOS, cpu, providerkit.NodePackageLibc, said.String())
		}
	}
	return copyTree(filepath.Join(staging, nodeModulesDirName), filepath.Join(funcDir, nodeModulesDirName), func(name string) bool {
		return strings.HasPrefix(name, ".")
	})
}

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
	return regexp.MustCompile(`(?:^|[/-])` + providerkit.NodePackageOS + `-` + regexp.QuoteMeta(cpu) + `(?:-|$)`).MatchString(dep)
}

func (m manifest) runsOn(cpu string) bool {
	return allows(m.OS, providerkit.NodePackageOS) && allows(m.CPU, cpu) && allows(m.Libc, providerkit.NodePackageLibc)
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

func npmInstallArgs(cpu string) []string {
	return []string{
		"install",
		"--os=" + providerkit.NodePackageOS,
		"--cpu=" + cpu,
		"--libc=" + providerkit.NodePackageLibc,
		"--ignore-scripts",
		"--no-bin-links",
		"--no-package-lock",
		"--no-audit",
		"--no-fund",
	}
}
