package discovery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
)

func gitIgnoredDirs(t *testing.T, repo string) map[string]bool {
	t.Helper()

	listed, err := exec.Command("git", "-C", repo, "ls-files",
		"--others", "--ignored", "--exclude-standard", "--directory").Output()
	if err != nil {
		t.Fatalf("ask git what it ignores: %v", err)
	}
	ignored := map[string]bool{}
	for line := range strings.Lines(string(listed)) {
		if rel := strings.TrimSuffix(strings.TrimSpace(line), "/"); rel != "" {
			ignored[rel] = true
		}
	}
	return ignored
}

func TestGoCodeNamesSharedPathsThroughConstants(t *testing.T) {
	repo := fixturetest.RepoDir(t)
	ignored := gitIgnoredDirs(t, repo)
	allowed := map[string]map[string]bool{
		"cli/internal/attribution/rust_test.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"cli/internal/cli/clitest/fakeprovider.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"cli/internal/discovery/rust_test.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"pkg/constants/constants.go": {
			constants.DefaultDiscoveryDirName: true,
			constants.ProjectStateDirName:     true,
			constants.PhaseEnvName:            true,
			constants.DevServerEnvName:        true,
			constants.DevServerTokenEnvName:   true,
			constants.AppFolderEnvName:        true,
			constants.AppURLEnvName:           true,
			constants.RuntimeAddressEnvName:   true,
		},
		"pkg/constants/constants_test.go": {
			constants.PhaseEnvName:          true,
			constants.DevServerEnvName:      true,
			constants.DevServerTokenEnvName: true,
			constants.AppFolderEnvName:      true,
			constants.AppURLEnvName:         true,
			constants.RuntimeAddressEnvName: true,
		},
		"pkg/naming/stack.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"pkg/providerkit/providerserver/eventstream_test.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"pkg/providerkit/provider/stacks.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"pkg/providerkit/pulumi/runtime.go": {
			constants.ProjectStateDirName: true,
		},
		"tests/fixtures/sdk/go/server/main.go": {
			"example.com/web/" + constants.DefaultDiscoveryDirName: true,
		},
	}
	segments := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[/\\"'])` + regexp.QuoteMeta(constants.DefaultDiscoveryDirName) + `(?:$|[/\\"'])`),
		regexp.MustCompile(`(?:^|[/\\"'])` + regexp.QuoteMeta(constants.ProjectStateDirName) + `(?:$|[/\\"'])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.PhaseEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.DevServerEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.DevServerTokenEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.AppFolderEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.AppURLEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.RuntimeAddressEnvName + `(?:$|[^A-Z0-9_])`),
	}
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".next", ".venv", ".claude", constants.ProjectStateDirName, "node_modules", "dist", "target":
				return filepath.SkipDir
			}
			if rel == "pkg/proto" || rel == "sdk" || ignored[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			for _, segment := range segments {
				if err == nil && segment.MatchString(value) {
					if allowed[rel][value] {
						continue
					}
					t.Errorf("%s names a shared path directly", rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryNamesTheDefaultDiscoveryDirectoryCentrally(t *testing.T) {
	repo := fixturetest.RepoDir(t)
	ignored := gitIgnoredDirs(t, repo)
	fixtureRoots := []string{
		"tests/fixtures/iac/with-pulumi",
		"tests/fixtures/iac/with-sst",
		"tests/fixtures/lifecycle/next",
		"tests/fixtures/sdk/go",
		"tests/fixtures/sdk/next",
		"tests/fixtures/sdk/node",
		"tests/fixtures/sdk/python",
		"tests/fixtures/sdk/with-transforms",
		"tests/fixtures/sdk/workspace",
	}
	for _, root := range fixtureRoots {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(root), constants.DefaultDiscoveryDirName)); err != nil {
			t.Errorf("%s does not name its discovery directory through the shared default: %v", root, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "tests", "fixtures", "sdk", "rust-workspace", "crates", constants.DefaultDiscoveryDirName)); err != nil {
		t.Errorf("the Rust workspace fixture does not name its discovery crate through the shared default: %v", err)
	}

	name := regexp.QuoteMeta(constants.DefaultDiscoveryDirName)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[^[:alnum:]_])` + name + `/`),
		regexp.MustCompile(`/` + name + `(?:$|[^[:alnum:]_])`),
		regexp.MustCompile(`\b` + name + `\s+folder\b`),
		regexp.MustCompile(`\bfrom\s+` + name + `\s+import\b`),
	}
	allowed := func(rel string) bool {
		if rel == "www/content/docs/configuration.mdx" || rel == "console/github/src/server.ts" || strings.HasPrefix(rel, constants.DefaultDiscoveryDirName+"/") || strings.HasPrefix(rel, "packages/ocel/tests/fixtures/"+constants.DefaultDiscoveryDirName+"/") {
			return true
		}
		for _, root := range append(fixtureRoots, "tests/fixtures/sdk/rust-workspace") {
			if strings.HasPrefix(rel, root+"/") && filepath.Ext(rel) != ".md" {
				return true
			}
		}
		return false
	}
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".next", ".venv", ".claude", constants.ProjectStateDirName, "node_modules", "dist", "target":
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				return err
			}
			if ignored[filepath.ToSlash(rel)] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if filepath.Ext(path) == ".go" || allowed(rel) {
			return nil
		}
		for _, part := range strings.Split(rel, "/") {
			if part == constants.DefaultDiscoveryDirName {
				t.Errorf("%s names the default discovery directory directly", rel)
			}
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, pattern := range patterns {
			if pattern.Match(contents) {
				t.Errorf("%s names the default discovery directory directly", rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGoSDKWireNamesMatchConstants(t *testing.T) {
	path := filepath.Join(fixturetest.RepoDir(t), "sdk", "wire.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Values) != len(spec.Names) {
			return true
		}
		for i, name := range spec.Names {
			if literal, ok := spec.Values[i].(*ast.BasicLit); ok && literal.Kind == token.STRING {
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				got[name.Name] = value
			}
		}
		return true
	})
	want := map[string]string{
		"phaseEnv":          constants.PhaseEnvName,
		"devServerEnv":      constants.DevServerEnvName,
		"devServerTokenEnv": constants.DevServerTokenEnvName,
		"appFolderEnv":      constants.AppFolderEnvName,
		"appURLEnv":         constants.AppURLEnvName,
		"runtimeAddressEnv": constants.RuntimeAddressEnvName,
		"liveDirEnv":        constants.LiveDirEnvName,
		"sessionTokenEnv":   channel.SessionTokenEnvVar,
		"sdkVersionHeader":  constants.SDKVersionHeader,
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("sdk/wire.go %s = %q, want %q", name, got[name], value)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("sdk/wire.go %s has no counterpart the CLI names", name)
		}
	}
}
