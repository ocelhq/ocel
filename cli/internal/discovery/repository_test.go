package discovery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/pkg/constants"
)

func TestGoCodeNamesSharedPathsThroughConstants(t *testing.T) {
	repo := fixturetest.RepoDir(t)
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
			constants.AppFolderEnvName:        true,
			constants.AppURLEnvName:           true,
			constants.RuntimeAddressEnvName:   true,
		},
		"pkg/constants/constants_test.go": {
			constants.PhaseEnvName:          true,
			constants.DevServerEnvName:      true,
			constants.AppFolderEnvName:      true,
			constants.AppURLEnvName:         true,
			constants.RuntimeAddressEnvName: true,
		},
		"pkg/naming/stack.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"pkg/providerkit/events_test.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"pkg/providerkit/pulumi/runtime.go": {
			constants.ProjectStateDirName: true,
		},
		"pkg/providerkit/release.go": {
			constants.DefaultDiscoveryDirName: true,
		},
		"sdk/env_test.go": {
			"ocel:\"" + constants.AppURLEnvName + "\"":           true,
			"ocel:\"" + constants.AppURLEnvName + ",sensitive\"": true,
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
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.AppFolderEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.AppURLEnvName + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + constants.RuntimeAddressEnvName + `(?:$|[^A-Z0-9_])`),
	}
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".next", ".venv", constants.ProjectStateDirName, "node_modules", "dist", "target":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
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
	fixtureRoots := []string{
		"tests/fixtures/lifecycle/next",
		"tests/fixtures/sdk/go",
		"tests/fixtures/sdk/next",
		"tests/fixtures/sdk/node",
		"tests/fixtures/sdk/python",
		"tests/fixtures/sdk/with-pulumi",
		"tests/fixtures/sdk/with-sst",
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
		if rel == "www/content/docs/configuration.mdx" || strings.HasPrefix(rel, "packages/ocel/tests/fixtures/"+constants.DefaultDiscoveryDirName+"/") {
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
			case ".git", ".next", ".venv", constants.ProjectStateDirName, "node_modules", "dist", "target":
				return filepath.SkipDir
			}
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

func TestGoCICoversTheSharedConstantsModule(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join(fixturetest.RepoDir(t), ".github", "workflows", "go.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "./pkg/constants/...") {
		t.Fatal("the Go CI matrix does not build and test the shared constants module")
	}
}
