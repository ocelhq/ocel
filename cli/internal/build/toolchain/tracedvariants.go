package toolchain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/arch"
)

type tracedPackage struct {
	root     string
	manifest manifest
}

func (p tracedPackage) nodeModules() string {
	parent := filepath.Dir(p.root)
	if isScopeDir(parent) {
		return filepath.Dir(parent)
	}
	return parent
}

func isScopeDir(dir string) bool {
	return strings.HasPrefix(filepath.Base(dir), "@")
}

func isPackageRoot(dir string) bool {
	parent := filepath.Dir(dir)
	if isScopeDir(parent) {
		parent = filepath.Dir(parent)
	}
	return filepath.Base(parent) == nodeModulesDir
}

type tracedTree struct {
	packages []tracedPackage
	links    []string
}

func readTracedTree(functionDir string) (tracedTree, error) {
	var traced tracedTree
	err := filepath.WalkDir(functionDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			traced.links = append(traced.links, path)
			return nil
		}
		if entry.IsDir() || entry.Name() != "package.json" || !isPackageRoot(filepath.Dir(path)) {
			return nil
		}
		pkg, err := readManifest(filepath.Dir(path))
		if err != nil || pkg.Name == "" {
			return nil
		}
		traced.packages = append(traced.packages, tracedPackage{root: filepath.Dir(path), manifest: pkg})
		return nil
	})
	return traced, err
}

type PlatformVariantCache struct {
	staged map[stagedVariantsKey]stagedVariants
}

type stagedVariantsKey struct {
	name, version, cpu, npmConfig string
}

type stagedVariants struct {
	dir      string
	variants []tracedPackage
}

func NewPlatformVariantCache() *PlatformVariantCache {
	return &PlatformVariantCache{staged: map[stagedVariantsKey]stagedVariants{}}
}

func (c *PlatformVariantCache) Close() error {
	var errs []error
	for key, install := range c.staged {
		errs = append(errs, os.RemoveAll(install.dir))
		delete(c.staged, key)
	}
	return errors.Join(errs...)
}

func (c *PlatformVariantCache) InstallTraced(ctx context.Context, app, source, functionDir, architecture string) error {
	cpu, known := arch.NodePackageCPU(architecture)
	if !known {
		return fmt.Errorf("app %q declares architecture %q, which has no npm cpu", app, architecture)
	}
	traced, err := readTracedTree(functionDir)
	if err != nil {
		return err
	}
	for _, parent := range traced.findPackagesWithTracedVariants() {
		if traced.hasVariantFor(parent, cpu) {
			continue
		}
		install, err := c.stage(ctx, app, source, parent.manifest, cpu)
		if err != nil {
			return err
		}
		if err := install.copyBeside(parent); err != nil {
			return err
		}
	}
	return traced.removeVariantsNotFor(cpu)
}

func (t tracedTree) findPackagesWithTracedVariants() []tracedPackage {
	var parents []tracedPackage
	for _, pkg := range t.packages {
		if pkg.manifest.platformVariant() {
			continue
		}
		if slices.ContainsFunc(t.packages, func(variant tracedPackage) bool {
			_, optional := pkg.manifest.OptionalDependencies[variant.manifest.Name]
			return optional && variant.manifest.platformVariant()
		}) {
			parents = append(parents, pkg)
		}
	}
	return parents
}

func (t tracedTree) hasVariantFor(parent tracedPackage, cpu string) bool {
	for dep := range parent.manifest.OptionalDependencies {
		if !namesVariantFor(dep, cpu) {
			continue
		}
		variant, err := readManifest(filepath.Join(parent.nodeModules(), filepath.FromSlash(dep)))
		if err == nil && variant.platformVariant() && variant.runsOn(cpu) {
			return true
		}
	}
	return false
}

func (c *PlatformVariantCache) stage(ctx context.Context, app, source string, parent manifest, cpu string) (stagedVariants, error) {
	config, hasConfig := projectNpmConfig(source)
	key := stagedVariantsKey{name: parent.Name, version: parent.Version, cpu: cpu, npmConfig: config}
	if install, staged := c.staged[key]; staged {
		return install, nil
	}
	if _, err := exec.LookPath(npmCommand); err != nil {
		return stagedVariants{}, fmt.Errorf("app %q traces %s, which ships one package per platform, and no %s is on PATH to install it for %s/%s: %w",
			app, parent.Name, npmCommand, arch.NodePackageOS, cpu, err)
	}
	staging, err := os.MkdirTemp("", "ocel-traced-platform-install-")
	if err != nil {
		return stagedVariants{}, err
	}
	install, err := installVariants(ctx, app, staging, parent, cpu, config, hasConfig)
	if err != nil {
		return stagedVariants{}, errors.Join(err, os.RemoveAll(staging))
	}
	c.staged[key] = install
	return install, nil
}

func installVariants(ctx context.Context, app, staging string, parent manifest, cpu, config string, hasConfig bool) (stagedVariants, error) {
	if err := writeJSON(filepath.Join(staging, "package.json"), map[string]any{"private": true, "dependencies": map[string]string{parent.Name: parent.Version}}); err != nil {
		return stagedVariants{}, err
	}
	if hasConfig {
		if err := copyFile(config, filepath.Join(staging, npmConfigFile), 0o600); err != nil {
			return stagedVariants{}, err
		}
	}
	cmd := exec.CommandContext(ctx, npmCommand, npmInstallArgs(cpu)...)
	cmd.Dir = staging
	var npmOutput bytes.Buffer
	cmd.Stdout = &npmOutput
	cmd.Stderr = &npmOutput
	if err := cmd.Run(); err != nil {
		return stagedVariants{}, fmt.Errorf("install %s for app %q on %s/%s with %s (%w):\n%s",
			parent.Name, app, arch.NodePackageOS, cpu, describeNpmConfig(config, hasConfig), err, npmOutput.String())
	}
	installed, err := readTracedTree(filepath.Join(staging, nodeModulesDir))
	if err != nil {
		return stagedVariants{}, err
	}
	install := stagedVariants{dir: staging}
	for _, pkg := range installed.packages {
		if pkg.manifest.platformVariant() && pkg.manifest.runsOn(cpu) {
			install.variants = append(install.variants, pkg)
		}
	}
	if !slices.ContainsFunc(install.variants, func(variant tracedPackage) bool { return namesVariantFor(variant.manifest.Name, cpu) }) {
		return stagedVariants{}, fmt.Errorf("npm installed %s for app %q without a package built for %s/%s/%s, so the function would fail at its first require of it; npm reported:\n%s",
			parent.Name, app, arch.NodePackageOS, cpu, arch.NodePackageLibc, npmOutput.String())
	}
	return install, nil
}

func (s stagedVariants) copyBeside(parent tracedPackage) error {
	for _, variant := range s.variants {
		dest := filepath.Join(parent.nodeModules(), filepath.FromSlash(variant.manifest.Name))
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		if err := copyTree(variant.root, dest, func(string) bool { return false }); err != nil {
			return err
		}
	}
	return nil
}

func (t tracedTree) removeVariantsNotFor(cpu string) error {
	var removed []string
	for _, pkg := range t.packages {
		if pkg.manifest.platformVariant() && !pkg.manifest.runsOn(cpu) {
			removed = append(removed, pkg.root)
		}
	}
	for _, link := range t.links {
		target, err := os.Readlink(link)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(link), target)
		}
		if slices.ContainsFunc(removed, func(root string) bool { return within(root, target) }) {
			removed = append(removed, link)
		}
	}
	for _, path := range removed {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
