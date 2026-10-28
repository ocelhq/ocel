package discovery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const importsAnUninstalledDependency = `import "fakedep";` + registerSideEffect

func fakeNpm(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	body := "#!/bin/sh\necho \"$@\" >> '" + calls + "'\n" + script
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake npm: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

const installsFakedep = `sleep 0.2
mkdir -p node_modules/fakedep
printf '{"name":"fakedep","version":"1.0.0","main":"index.js"}' > node_modules/fakedep/package.json
printf 'module.exports = "installed";' > node_modules/fakedep/index.js
`

func uninstalledProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(root, "package.json"), `{"name":"app","dependencies":{"fakedep":"1.0.0"}}`)
	write(t, filepath.Join(root, "package-lock.json"), "{}")
	write(t, filepath.Join(root, DefaultRootDirName, "main.ts"), importsAnUninstalledDependency)
	return root
}

func prepareWithOutput(t *testing.T, root string) (Programs, string, error) {
	t.Helper()
	roots, err := Roots(root, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	var output bytes.Buffer
	prepared, err := Prepare(context.Background(), root, roots, &output)
	return prepared, output.String(), err
}

func readCalls(t *testing.T, calls string) []string {
	t.Helper()
	read, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the fake npm's calls: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(read)), "\n")
}

func TestDiscoveryInstallsDependenciesNothingHasInstalledBeforeBundling(t *testing.T) {
	calls := fakeNpm(t, installsFakedep)
	root := uninstalledProject(t)

	_, output, err := prepareWithOutput(t, root)
	if err != nil {
		t.Fatalf("Prepare() = %v, want the bundle to resolve the dependency the install wrote", err)
	}

	if got := readCalls(t, calls); len(got) != 1 || got[0] != "ci" {
		t.Errorf("npm ran %q, want one clean install from the lockfile", got)
	}
	if want := "Installing the dependencies in " + root + " with `npm ci`"; !strings.Contains(output, want) {
		t.Errorf("output = %q, want the line %q", output, want)
	}
}

func TestDiscoveryLeavesDependenciesAlreadyInstalledAsTheyAre(t *testing.T) {
	calls := fakeNpm(t, "exit 1\n")
	root := uninstalledProject(t)
	write(t, filepath.Join(root, "node_modules", "fakedep", "package.json"), `{"name":"fakedep","version":"1.0.0","main":"index.js"}`)
	write(t, filepath.Join(root, "node_modules", "fakedep", "index.js"), `module.exports = "installed";`)

	_, output, err := prepareWithOutput(t, root)
	if err != nil {
		t.Fatalf("Prepare() = %v", err)
	}

	if got := readCalls(t, calls); got != nil {
		t.Errorf("npm ran %q over a node_modules already installed", got)
	}
	if output != "" {
		t.Errorf("output = %q, want nothing said when nothing is installed", output)
	}
}

func TestAFailedInstallIsRefusedWithTheCommandToRunAndWhatItPrinted(t *testing.T) {
	fakeNpm(t, "echo 'npm error the lockfile does not satisfy fakedep@1.0.0' >&2\nexit 1\n")
	root := uninstalledProject(t)

	_, _, err := prepareWithOutput(t, root)
	if err == nil {
		t.Fatal("Prepare() succeeded with an install that failed")
	}
	for _, want := range []string{"`npm ci`", root, "the lockfile does not satisfy fakedep@1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Prepare() = %v, and the reader is never told %q", err, want)
		}
	}
}

func TestConcurrentDiscoveriesOfOneProjectInstallItsDependenciesOnce(t *testing.T) {
	calls := fakeNpm(t, installsFakedep)
	root := uninstalledProject(t)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { _, _, errs[i] = prepareWithOutput(t, root) })
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("Prepare() = %v", err)
		}
	}
	if got := readCalls(t, calls); len(got) != 1 {
		t.Errorf("npm ran %d times (%q), want the first run to install and the rest to find it installed", len(got), got)
	}
}
