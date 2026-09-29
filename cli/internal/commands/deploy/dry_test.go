package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployrecord"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func newDryRunDependencies(t *testing.T) Dependencies {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{{
		Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js",
		ArtifactPath: "output/api", App: "api",
	}})
	return dependencies
}

var planRows = []string{
	"main  postgres",
	"values",
	"artifact",
	"direct/edge",
	"promotion",
	"Run without --dry to apply.",
}

func absent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func TestADryDeployShowsThePlanAndWritesNothing(t *testing.T) {
	dependencies := newDryRunDependencies(t)
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range planRows {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want the plan to show %q", out, want)
		}
	}
	if strings.Contains(out, "Deployed") {
		t.Errorf("stdout = %q, want a dry run to report a plan, never a deploy", out)
	}
	if !strings.Contains(out, "✓ Planned the deploy of "+clitest.FixtureSlug+" to production in ") {
		t.Errorf("stdout = %q, want the run to end naming what it planned and where", out)
	}
	if !absent(t, deployrecord.Path(root)) {
		t.Error("a dry deploy wrote the deploy result, want a run that records nothing it did not do")
	}
}

func TestADryPreviewUpShowsThePlanAndWritesNothing(t *testing.T) {
	dependencies := newDryRunDependencies(t)
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")
	t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging", dry: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range planRows {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want the plan to show %q", out, want)
		}
	}
	if strings.Contains(out, "Deployed ") {
		t.Errorf("stdout = %q, want a dry run to report a plan, never a preview being provisioned", out)
	}
	if !absent(t, deployrecord.Path(root)) {
		t.Error("a dry preview up wrote the deploy result, want a run that records nothing it did not do")
	}
}

func TestADryRunRefusesOnAnUnbootstrappedAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    func(dependencies Dependencies, root string, stdout, stderr *bytes.Buffer) error
		remedy string
	}{
		{
			name: "deploy",
			run: func(dependencies Dependencies, root string, stdout, stderr *bytes.Buffer) error {
				clitest.AttachTerminalSink(dependencies.Invocation, stdout)
				return runDeploy(context.Background(), dependencies, root, deployOptions{dry: true}, stdout, stderr, strings.NewReader(""))
			},
			remedy: "ocel bootstrap production",
		},
		{
			name: "preview up",
			run: func(dependencies Dependencies, root string, stdout, stderr *bytes.Buffer) error {
				clitest.AttachTerminalSink(dependencies.Invocation, stdout)
				return runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging", dry: true}, stdout, stderr, strings.NewReader(""))
			},
			remedy: "ocel bootstrap preview",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dependencies := newDryRunDependencies(t)
			root, _ := clitest.SetUpDeployFixture(t)
			addAppToFixtureConfig(t, root)
			t.Setenv(clitest.FakeInfraPresentEnvVar, "0")

			var stdout, stderr bytes.Buffer
			err := tc.run(dependencies, root, &stdout, &stderr)
			if err == nil {
				t.Fatalf("run err = nil, want a refusal; stdout=%s", stdout.String())
			}
			if !strings.Contains(stdout.String(), tc.remedy) {
				t.Errorf("stdout = %q, want the refusal to name the remedy %q", stdout.String(), tc.remedy)
			}
			if strings.Contains(stdout.String(), "Run without --dry to apply.") {
				t.Errorf("stdout = %q, want no plan drawn against an account that has none", stdout.String())
			}
		})
	}
}

func TestADryRunRefusesABootstrapThatIsBehindTheBuild(t *testing.T) {
	dependencies := newDryRunDependencies(t)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")
	t.Setenv(clitest.FakeBootstrapEnvVar, "stale")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want a refusal; stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run `ocel bootstrap production --features") {
		t.Errorf("stdout = %q, want the refusal to name the bootstrap that would have to run first", stdout.String())
	}
	if strings.Contains(stdout.String(), "Run without --dry to apply.") {
		t.Errorf("stdout = %q, want no plan drawn against a bootstrap that could not serve it", stdout.String())
	}
	if strings.Contains(stdout.String(), "now?") {
		t.Errorf("stdout = %q, want a dry run never to offer to bootstrap: it changes nothing", stdout.String())
	}
}

func TestADryRunNeverOpensTheVariableEditor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preview bool
		run     func(dependencies Dependencies, root string, stdout, stderr *bytes.Buffer) error
	}{
		{
			name: "deploy",
			run: func(dependencies Dependencies, root string, stdout, stderr *bytes.Buffer) error {
				clitest.AttachTerminalSink(dependencies.Invocation, stdout)
				return runDeploy(context.Background(), dependencies, root, deployOptions{dry: true}, stdout, stderr, strings.NewReader(""))
			},
		},
		{
			name:    "preview up",
			preview: true,
			run: func(dependencies Dependencies, root string, stdout, stderr *bytes.Buffer) error {
				clitest.AttachTerminalSink(dependencies.Invocation, stdout)
				return runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging", dry: true}, stdout, stderr, strings.NewReader(""))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := clitest.SetUpVariablesFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
			t.Setenv("OCEL_TEST_ENV_PROBLEMS", `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`)
			if tc.preview {
				t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
			}
			dependencies := newTestDependencies()
			terminalStdin(&dependencies)
			served := 0
			dependencies.ServeVariableEditor = func(context.Context, *project.Project, *providerclient.Provider, environmentv1.Tier, *variables.Declarations, *variableeditor.Recovery) (*variableeditor.Session, error) {
				served++
				return nil, errors.New("a dry run must never serve the variables UI")
			}

			var stdout, stderr bytes.Buffer
			err := tc.run(dependencies, root, &stdout, &stderr)
			if err == nil {
				t.Fatalf("run err = nil, want the declarations to refuse; stdout=%s", stdout.String())
			}
			if served != 0 {
				t.Errorf("a dry run served the variables UI %d times, want none: a value written through it changes the account", served)
			}
			if !strings.Contains(stdout.String(), "STRIPE_API_KEY") {
				t.Errorf("stdout = %q, want the refusal to name the variable the plan has no value for", stdout.String())
			}
		})
	}
}

func TestADryRunRefusesWhenTheBootstrapLacksWhatTheProjectNeeds(t *testing.T) {
	dependencies := newDryRunDependencies(t)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want a refusal; stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run `ocel bootstrap production --features") {
		t.Errorf("stdout = %q, want the bootstrap that would have to run first", stdout.String())
	}
	if strings.Contains(stdout.String(), "now?") {
		t.Errorf("stdout = %q, want a dry run never to offer to bootstrap: it changes nothing", stdout.String())
	}
}

func projectFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	state := filepath.Join(root, statedir.Name)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == state {
				return fs.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		files[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func changedFiles(before, after map[string]string) []string {
	var changed []string
	for path, digest := range after {
		switch was, ok := before[path]; {
		case !ok:
			changed = append(changed, path+" was written")
		case was != digest:
			changed = append(changed, path+" was rewritten")
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, path+" was removed")
		}
	}
	slices.Sort(changed)
	return changed
}

func writeAppTSConfig(t *testing.T, root, app string) string {
	t.Helper()
	path := filepath.Join(root, "apps", app, "tsconfig.json")
	clitest.WriteFile(t, path, "{\n  \"compilerOptions\": { \"strict\": true }\n}\n")
	return path
}

func TestADryDeployLeavesEveryFileTheProjectOwnsAsItFoundIt(t *testing.T) {
	dependencies := newDryRunDependencies(t)
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")
	writeAppTSConfig(t, root, "api")

	before := projectFiles(t, root)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Run without --dry to apply.") {
		t.Fatalf("stdout = %q, want the plan a dry run exists to draw", stdout.String())
	}

	if changed := changedFiles(before, projectFiles(t, root)); len(changed) > 0 {
		t.Errorf("a dry deploy changed the project's own files: %s", strings.Join(changed, ", "))
	}
}

func TestADeployPointsEachAppsImportsAtItsClientAccessor(t *testing.T) {
	dependencies := newDryRunDependencies(t)
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")
	tsconfig := writeAppTSConfig(t, root, "api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	if _, err := os.Stat(filepath.Join(root, statedir.Name, "apps", "api", "env-client.ts")); err != nil {
		t.Fatalf("no client accessor was generated: %v", err)
	}
	mapping := `"ocel/env/client": ["../../` + statedir.Name + `/apps/api/env-client.ts"]`
	data, err := os.ReadFile(tsconfig)
	if err != nil {
		t.Fatalf("read %s: %v", tsconfig, err)
	}
	if !strings.Contains(string(data), mapping) {
		t.Errorf("tsconfig.json = %s, want it to state %s", data, mapping)
	}
}
