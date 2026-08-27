package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	"github.com/ocelhq/ocel/cli/internal/varsui"
)

func dryDeps(t *testing.T) cmddeps.Deps {
	t.Helper()
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{{
		Route: "api", Runtime: "nodejs24.x", Handler: "src/server.js",
		ArtifactPath: "output/api", Framework: "express", App: "api",
	}})
	return deps
}

var planRows = []string{
	"main  postgres",
	"values",
	"artifact",
	"cloudfront/edge",
	"promotion",
	"Run without --dry to apply.",
}

func absent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func TestADryDeployShowsThePlanAndWritesNothing(t *testing.T) {
	deps := dryDeps(t)
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))
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
	if !absent(t, deployresult.Path(root)) {
		t.Error("a dry deploy wrote the deploy result, want a run that records nothing it did not do")
	}
	if !absent(t, servicemap.Path(root)) {
		t.Error("a dry deploy published the service map, want a run that records nothing it did not do")
	}
}

func TestADryPreviewUpShowsThePlanAndWritesNothing(t *testing.T) {
	deps := dryDeps(t)
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	writeServeDescriptor(t, root, "api", "bld_api_1")
	t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

	var stdout, stderr bytes.Buffer
	err := runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "staging", dry: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range planRows {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want the plan to show %q", out, want)
		}
	}
	if strings.Contains(out, "is up") {
		t.Errorf("stdout = %q, want a dry run to report a plan, never a preview standing up", out)
	}
	if !absent(t, deployresult.Path(root)) {
		t.Error("a dry preview up wrote the deploy result, want a run that records nothing it did not do")
	}
	if !absent(t, servicemap.Path(root)) {
		t.Error("a dry preview up published the service map, want a run that records nothing it did not do")
	}
}

func TestADryRunRefusesOnAnUnbootstrappedAccount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    func(deps cmddeps.Deps, root string, stdout, stderr *bytes.Buffer) error
		remedy string
	}{
		{
			name: "deploy",
			run: func(deps cmddeps.Deps, root string, stdout, stderr *bytes.Buffer) error {
				return runDeploy(context.Background(), deps, root, deployOptions{dry: true}, stdout, stderr, strings.NewReader(""))
			},
			remedy: "ocel bootstrap production",
		},
		{
			name: "preview up",
			run: func(deps cmddeps.Deps, root string, stdout, stderr *bytes.Buffer) error {
				return runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "staging", dry: true}, stdout, stderr, strings.NewReader(""))
			},
			remedy: "ocel bootstrap preview",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := dryDeps(t)
			root, _ := clitest.SetUpDeployFixture(t)
			addAppToFixtureConfig(t, root)
			t.Setenv(clitest.FakeInfraPresentEnvVar, "0")

			var stdout, stderr bytes.Buffer
			err := tc.run(deps, root, &stdout, &stderr)
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

func TestADryRunNeverOpensTheVarsUI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preview bool
		run     func(deps cmddeps.Deps, root string, stdout, stderr *bytes.Buffer) error
	}{
		{
			name: "deploy",
			run: func(deps cmddeps.Deps, root string, stdout, stderr *bytes.Buffer) error {
				return runDeploy(context.Background(), deps, root, deployOptions{dry: true}, stdout, stderr, strings.NewReader(""))
			},
		},
		{
			name:    "preview up",
			preview: true,
			run: func(deps cmddeps.Deps, root string, stdout, stderr *bytes.Buffer) error {
				return runPreviewUp(context.Background(), deps, root, previewUpOptions{name: "staging", dry: true}, stdout, stderr, strings.NewReader(""))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := clitest.SetUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
			t.Setenv("OCEL_TEST_ENV_PROBLEMS", `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`)
			if tc.preview {
				t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
			}
			deps := clitest.NewDeps()
			terminalStdin(&deps)
			served := 0
			deps.ServeVarsUI = func(context.Context, *projectconfig.Config, *provider.Runner, bool, *envgate.Gate) (*varsui.Session, error) {
				served++
				return nil, errors.New("a dry run must never serve the variables UI")
			}

			var stdout, stderr bytes.Buffer
			err := tc.run(deps, root, &stdout, &stderr)
			if err == nil {
				t.Fatalf("run err = nil, want the gate to refuse; stdout=%s", stdout.String())
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
	deps := dryDeps(t)
	deps.StdinIsTerminal = func(io.Reader) bool { return true }
	root, _ := clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	t.Setenv(clitest.FakeBootstrapEnvVar, "missing")

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))
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
