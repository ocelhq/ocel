package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/shellword"
)

var installedMarkers = []string{vendorDir, ".pnp.cjs", ".pnp.js"}

type Install struct {
	Dir  string
	Argv []string
}

func (i Install) Command() string {
	quoted := make([]string, len(i.Argv))
	for n, word := range i.Argv {
		quoted[n] = shellword.Quote(word)
	}
	return strings.Join(quoted, " ")
}

func FindMissingInstall(dir string) (Install, bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Install{}, false, err
	}
	pkg, ok := nearestPackage(abs)
	if !ok {
		return Install{}, false, nil
	}
	root := pkg
	if enclosingRoot, ok := enclosing(pkg); ok {
		root = enclosingRoot
	}
	declared, err := declaresDependencies(pkg, root)
	if err != nil || !declared || isInstalledFrom(pkg) {
		return Install{}, false, err
	}
	argv := behaviours[detect(root)].frozenInstall
	if argv == nil {
		return Install{}, false, fmt.Errorf(
			"the dependencies of %s are not installed, and %s holds no lockfile to install them from: install them in %s with your package manager, and commit the lockfile it writes",
			pkg, root, root,
		)
	}
	return Install{Dir: root, Argv: argv}, true, nil
}

func nearestPackage(dir string) (string, bool) {
	for ; ; dir = filepath.Dir(dir) {
		if isRegularFile(filepath.Join(dir, manifestName)) {
			return dir, true
		}
		if atBoundary(dir) {
			return "", false
		}
	}
}

func declaresDependencies(dirs ...string) (bool, error) {
	for _, dir := range dirs {
		m, err := readManifest(filepath.Join(dir, manifestName))
		if err != nil {
			return false, fmt.Errorf("read the %s in %s: %w", manifestName, dir, err)
		}
		if len(m.Dependencies) > 0 || len(m.DevDeps) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func isInstalledFrom(pkg string) bool {
	for dir := pkg; ; dir = filepath.Dir(dir) {
		for _, marker := range installedMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return true
			}
		}
		if atBoundary(dir) {
			return false
		}
	}
}
