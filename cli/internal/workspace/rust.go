package workspace

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ocelhq/ocel/cli/internal/cargo"
	"github.com/ocelhq/ocel/cli/internal/shellword"
)

const (
	cargoManifestFile = "Cargo.toml"
	rustBinDir        = "bin"
	rustTargetDir     = "target"
	rustMainFile      = "src/main.rs"
	rustBinSourcesDir = "src/bin"
	rustTool          = "rust"
)

type cargoDependencies map[string]any

type cargoManifest struct {
	Package *struct {
		Name        string `toml:"name"`
		DefaultRun  string `toml:"default-run"`
		Autobins    *bool  `toml:"autobins"`
		Workspace   string `toml:"workspace"`
		RustVersion any    `toml:"rust-version"`
	} `toml:"package"`
	Bin []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
	Dependencies      cargoDependencies `toml:"dependencies"`
	DevDependencies   cargoDependencies `toml:"dev-dependencies"`
	BuildDependencies cargoDependencies `toml:"build-dependencies"`
	Target            map[string]struct {
		Dependencies      cargoDependencies `toml:"dependencies"`
		DevDependencies   cargoDependencies `toml:"dev-dependencies"`
		BuildDependencies cargoDependencies `toml:"build-dependencies"`
	} `toml:"target"`
	Patch     map[string]cargoDependencies `toml:"patch"`
	Workspace *struct {
		Dependencies cargoDependencies `toml:"dependencies"`
		Package      struct {
			RustVersion string `toml:"rust-version"`
		} `toml:"package"`
	} `toml:"workspace"`
}

func readCargoManifest(dir string) (cargoManifest, bool, error) {
	named := filepath.Join(dir, cargoManifestFile)
	read, err := os.ReadFile(named)
	if os.IsNotExist(err) {
		return cargoManifest{}, false, nil
	}
	if err != nil {
		return cargoManifest{}, false, fmt.Errorf("read %s: %w", named, err)
	}
	var manifest cargoManifest
	if err := toml.Unmarshal(read, &manifest); err != nil {
		return cargoManifest{}, false, fmt.Errorf("read %s: %w", named, err)
	}
	return manifest, true, nil
}

func (m cargoManifest) listPathDependencies(dir string) []string {
	tables := []cargoDependencies{m.Dependencies, m.DevDependencies, m.BuildDependencies}
	for _, target := range m.Target {
		tables = append(tables, target.Dependencies, target.DevDependencies, target.BuildDependencies)
	}
	for _, patched := range m.Patch {
		tables = append(tables, patched)
	}
	if m.Workspace != nil {
		tables = append(tables, m.Workspace.Dependencies)
	}
	var dirs []string
	for _, table := range tables {
		for _, declared := range table {
			if named, ok := findLocalPath(dir, declared); ok {
				dirs = append(dirs, named)
			}
		}
	}
	return dirs
}

func findCargoWorkspaceRoot(dir string, manifest cargoManifest, repository string) (string, bool, error) {
	if manifest.Workspace != nil {
		return "", false, nil
	}
	if manifest.Package != nil && manifest.Package.Workspace != "" {
		return filepath.Clean(filepath.Join(dir, filepath.FromSlash(manifest.Package.Workspace))), true, nil
	}
	for at := dir; at != repository && under(repository, at); {
		at = filepath.Dir(at)
		enclosing, ok, err := readCargoManifest(at)
		if err != nil {
			return "", false, err
		}
		if ok && enclosing.Workspace != nil {
			return at, true, nil
		}
	}
	return "", false, nil
}

func findRustContext(dir string) (string, error) {
	repository := findRepository(dir)
	var reached []string
	seen := map[string]bool{dir: true}
	for queue := []string{dir}; len(queue) > 0; queue = queue[1:] {
		crate := queue[0]
		manifest, ok, err := readCargoManifest(crate)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		next := manifest.listPathDependencies(crate)
		root, inWorkspace, err := findCargoWorkspaceRoot(crate, manifest, repository)
		if err != nil {
			return "", err
		}
		if inWorkspace {
			next = append(next, root)
		}
		for _, found := range next {
			if !seen[found] {
				seen[found] = true
				reached = append(reached, found)
				queue = append(queue, found)
			}
		}
	}
	root, outside := findContext(dir, reached)
	if outside != "" {
		return "", fmt.Errorf(
			"the crate at %s reaches %s by path, and the only directory holding both is %s, outside %s: an image is built from one directory holding the crate and every crate it reaches by path, and never from above the repository the app is checked out in or from the filesystem root — move %s inside %s, or depend on a published version of the crate it holds",
			dir, outside, root, repository, outside, repository,
		)
	}
	return root, nil
}

func findRustBinary(dir string) (string, error) {
	manifest, _, err := readCargoManifest(dir)
	if err != nil {
		return "", err
	}
	crate := cargo.Package{}
	if manifest.Package != nil {
		crate.Name, crate.DefaultRun = manifest.Package.Name, manifest.Package.DefaultRun
	}
	named := map[string]bool{}
	claimed := map[string]bool{}
	addBin := func(name string) {
		if name != "" && !named[name] {
			named[name] = true
			crate.Targets = append(crate.Targets, cargo.Target{Name: name, Kind: []string{"bin"}})
		}
	}
	for _, bin := range manifest.Bin {
		name := bin.Name
		if name == "" {
			name = crate.Name
		}
		addBin(name)
		claimed[path.Clean(bin.Path)] = true
	}
	if manifest.Package == nil || manifest.Package.Autobins == nil || *manifest.Package.Autobins {
		if isRegularFile(filepath.Join(dir, filepath.FromSlash(rustMainFile))) && !claimed[rustMainFile] {
			addBin(crate.Name)
		}
		entries, _ := os.ReadDir(filepath.Join(dir, filepath.FromSlash(rustBinSourcesDir)))
		for _, entry := range entries {
			source := path.Join(rustBinSourcesDir, entry.Name())
			switch {
			case entry.IsDir() && isRegularFile(filepath.Join(dir, filepath.FromSlash(source), "main.rs")):
				if !claimed[path.Join(source, "main.rs")] {
					addBin(entry.Name())
				}
			case !entry.IsDir() && strings.HasSuffix(entry.Name(), ".rs") && !claimed[source]:
				addBin(strings.TrimSuffix(entry.Name(), ".rs"))
			}
		}
	}
	bin, err := crate.FindBin("")
	if err != nil {
		return "", fmt.Errorf("plan the rust app's build in %s: %w", dir, err)
	}
	return bin.Name, nil
}

func findRustVersion(dir string) (string, error) {
	manifest, _, err := readCargoManifest(dir)
	if err != nil || manifest.Package == nil {
		return "", err
	}
	if declared, ok := manifest.Package.RustVersion.(string); ok {
		return declared, nil
	}
	inherited, _ := manifest.Package.RustVersion.(map[string]any)
	if inherited["workspace"] != true {
		return "", nil
	}
	if manifest.Workspace != nil {
		return manifest.Workspace.Package.RustVersion, nil
	}
	root, ok, err := findCargoWorkspaceRoot(dir, manifest, findRepository(dir))
	if err != nil || !ok {
		return "", err
	}
	enclosing, _, err := readCargoManifest(root)
	if err != nil || enclosing.Workspace == nil {
		return "", err
	}
	return enclosing.Workspace.Package.RustVersion, nil
}

func (l Location) planRustCommands() (Commands, error) {
	belowContext := l.Path != "."
	bin := l.Entrypoint
	if bin == "" {
		if !belowContext {
			return Commands{}, nil
		}
		found, err := findRustBinary(l.Dir())
		if err != nil {
			return Commands{}, err
		}
		bin = found
	}
	target := path.Join(findRelativeRoot(l.Path), rustTargetDir)
	build := fmt.Sprintf("mkdir -p %s && cargo build --release --bin %s --target-dir %s && cp %s %s/", rustBinDir, shellword.Quote(bin), shellword.Quote(target), shellword.Quote(path.Join(target, "release", bin)), rustBinDir)
	commands := Commands{Build: l.inAppDir(build), Start: shellword.Quote("./" + path.Join(l.Path, rustBinDir, bin)), InstallsInBuild: belowContext}
	if belowContext {
		version, err := findRustVersion(l.Dir())
		if err != nil {
			return Commands{}, err
		}
		if version != "" {
			commands.Tools = []Tool{{Name: rustTool, Version: version}}
		}
	}
	return commands, nil
}
