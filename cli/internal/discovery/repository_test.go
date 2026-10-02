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
	"github.com/ocelhq/ocel/cli/internal/sdkversion"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/statedir"
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
			DefaultRootDirName: true,
		},
		"cli/internal/clitest/fakeprovider.go": {
			DefaultRootDirName: true,
		},
		"cli/internal/discovery/rust_test.go": {
			DefaultRootDirName: true,
		},
		"cli/internal/discovery/roots.go": {
			DefaultRootDirName: true,
		},
		"pkg/statedir/statedir.go": {
			statedir.Name: true,
		},
		"pkg/processenv/processenv.go": {
			processenv.PhaseEnvVar:          true,
			processenv.DevServerEnvVar:      true,
			processenv.DevServerTokenEnvVar: true,
			processenv.AppFolderEnvVar:      true,
			processenv.AppURLEnvVar:         true,
			processenv.RuntimeAddressEnvVar: true,
		},
		"pkg/processenv/processenv_test.go": {
			processenv.PhaseEnvVar:          true,
			processenv.DevServerEnvVar:      true,
			processenv.DevServerTokenEnvVar: true,
			processenv.AppFolderEnvVar:      true,
			processenv.AppURLEnvVar:         true,
			processenv.RuntimeAddressEnvVar: true,
		},
		"pkg/naming/stack.go": {
			DefaultRootDirName: true,
		},
		"pkg/provider/providerserver/eventstream_test.go": {
			DefaultRootDirName: true,
		},
		"pkg/provider/stacks.go": {
			DefaultRootDirName: true,
		},
		"pkg/provider/pulumi/runtime.go": {
			statedir.Name: true,
		},
		"platform/aws/provider/deploy_awslive_test.go": {
			DefaultRootDirName: true,
		},
		"tests/fixtures/realtime/go/server/main.go": {
			"example.com/realtime/" + DefaultRootDirName: true,
		},
		"tests/fixtures/sdk/go/server/main.go": {
			"example.com/web/" + DefaultRootDirName: true,
		},
		"tests/fixtures/tasks/go/server/main.go": {
			"example.com/tasks/" + DefaultRootDirName: true,
		},
	}
	segments := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[/\\"'])` + regexp.QuoteMeta(DefaultRootDirName) + `(?:$|[/\\"'])`),
		regexp.MustCompile(`(?:^|[/\\"'])` + regexp.QuoteMeta(statedir.Name) + `(?:$|[/\\"'])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + processenv.PhaseEnvVar + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + processenv.DevServerEnvVar + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + processenv.DevServerTokenEnvVar + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + processenv.AppFolderEnvVar + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + processenv.AppURLEnvVar + `(?:$|[^A-Z0-9_])`),
		regexp.MustCompile(`(?:^|[^A-Z0-9_])` + processenv.RuntimeAddressEnvVar + `(?:$|[^A-Z0-9_])`),
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
			case ".git", ".next", ".venv", ".claude", statedir.Name, "node_modules", "dist", "target":
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
		"tests/fixtures/kv/node",
		"tests/fixtures/kv/node-overlap",
		"tests/fixtures/lifecycle/next",
		"tests/fixtures/realtime/go",
		"tests/fixtures/realtime/node",
		"tests/fixtures/realtime/python",
		"tests/fixtures/sdk/go",
		"tests/fixtures/sdk/next",
		"tests/fixtures/sdk/node",
		"tests/fixtures/sdk/python",
		"tests/fixtures/sdk/with-transforms",
		"tests/fixtures/sdk/workspace",
		"tests/fixtures/tasks/go",
		"tests/fixtures/tasks/node",
		"tests/fixtures/worker/go",
		"tests/fixtures/worker/node",
		"tests/fixtures/worker/python",
	}
	for _, root := range fixtureRoots {
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(root), DefaultRootDirName)); err != nil {
			t.Errorf("%s does not name its discovery directory through the shared default: %v", root, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "tests", "fixtures", "sdk", "rust-workspace", "crates", DefaultRootDirName)); err != nil {
		t.Errorf("the Rust workspace fixture does not name its discovery crate through the shared default: %v", err)
	}

	name := regexp.QuoteMeta(DefaultRootDirName)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[^[:alnum:]_])` + name + `/`),
		regexp.MustCompile(`/` + name + `(?:$|[^[:alnum:]_])`),
		regexp.MustCompile(`\b` + name + `\s+folder\b`),
		regexp.MustCompile(`\bfrom\s+` + name + `\s+import\b`),
	}
	allowed := func(rel string) bool {
		if rel == "www/content/docs/configuration.mdx" || rel == "console/github/src/server.ts" || strings.HasPrefix(rel, DefaultRootDirName+"/") || strings.HasPrefix(rel, "packages/ocel/tests/fixtures/"+DefaultRootDirName+"/") {
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
			case ".git", ".next", ".venv", ".claude", statedir.Name, "node_modules", "dist", "target":
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
			if part == DefaultRootDirName {
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
		"phaseEnv":          processenv.PhaseEnvVar,
		"devServerEnv":      processenv.DevServerEnvVar,
		"devServerTokenEnv": processenv.DevServerTokenEnvVar,
		"appFolderEnv":      processenv.AppFolderEnvVar,
		"appURLEnv":         processenv.AppURLEnvVar,
		"runtimeAddressEnv": processenv.RuntimeAddressEnvVar,
		"liveDirEnv":        processenv.LiveDirEnvVar,
		"sessionTokenEnv":   localrpc.SessionTokenEnvVar,
		"sdkVersionHeader":  sdkversion.Header,
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
