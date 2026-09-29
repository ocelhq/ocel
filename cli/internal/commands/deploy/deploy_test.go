package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployrecord"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestDeployRefusesBeforeStartingAProviderWhenTheConfigCannotNameOne(t *testing.T) {
	t.Run("a missing config errors before any spawn", func(t *testing.T) {
		err := runDeploy(context.Background(), newTestDependencies(), t.TempDir(), deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %v, want it to hint at `ocel init`", err)
		}
	})

	t.Run("a malformed config errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `this is not valid TypeScript {{{`)

		err := runDeploy(context.Background(), newTestDependencies(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "ocel.config.ts") {
			t.Fatalf("err = %v, want it to mention ocel.config.ts", err)
		}
	})

	t.Run("no provider configured errors before any spawn", func(t *testing.T) {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
};
`)

		err := runDeploy(context.Background(), newTestDependencies(), root, deployOptions{yes: true}, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want error")
		}
		if !strings.Contains(err.Error(), "provider") {
			t.Fatalf("err = %v, want it to mention the missing provider", err)
		}
	})

	t.Run("the happy path discovers, builds, spawns and deploys to success", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()

		t.Run("streams the provider's progress", func(t *testing.T) {
			if !strings.Contains(out, "provisioning...") {
				t.Errorf("stdout = %q, want it to contain the streamed progress event", out)
			}
		})
		t.Run("ends on a terminal success message", func(t *testing.T) {
			if !strings.Contains(out, "Deployed") {
				t.Errorf("stdout = %q, want a terminal success message", out)
			}
		})
		t.Run("sends a production environment", func(t *testing.T) {
			if !strings.Contains(out, "DEPLOY tier=TIER_PRODUCTION lifecycle=LIFECYCLE_UNSPECIFIED") {
				t.Errorf("stdout = %q, want deploy to send a production Environment", out)
			}
		})
		t.Run("skips the confirm prompt under --yes", func(t *testing.T) {
			if strings.Contains(out, "[y/N]") {
				t.Errorf("stdout = %q, want the confirm prompt skipped by --yes", out)
			}
		})

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("an app builds its functions into the manifest", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{
				Route:        "api",
				Framework:    buildoutput.Framework{Name: "node"},
				EntryFile:    "src/server.js",
				ArtifactPath: "output/api",
				App:          "api",
			},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "FUNCTION logical_name=fn--api--api framework=node entry_file=src/server.js artifact_path=output/api app=api") {
			t.Errorf("stdout = %q, want the function to have reached the manifest", out)
		}
		if strings.Contains(stderr.String(), "deploying infrastructure only") {
			t.Errorf("stderr = %q, want no infra-only warning when a function is built", stderr.String())
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want a terminal success message", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("no apps warns and deploys resources only", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "No app has a function or image to deploy, so this deploys only the 1 resource test-app declares") {
			t.Errorf("stdout = %q, want the infra-only note naming how many resources deploy", out)
		}
		if !strings.Contains(out, "INFO  [build] ✓ test-app: Collected the resources test-app declares in ") {
			t.Errorf("stdout = %q, want the build unit to say it only collects what the project declares", out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want resources to still deploy to success", out)
		}
		if strings.Contains(out, "FUNCTION ") {
			t.Errorf("stdout = %q, want no function echoed when no apps are configured", out)
		}
		if strings.Contains(out, "APP ") {
			t.Errorf("stdout = %q, want no app echoed when nothing was built", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("an app build failure aborts before spawn", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
			return build.Output{}, errors.New("boom: app build failed")
		}
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the app-build failure")
		}
		if !strings.Contains(stdout.String(), "boom: app build failed") {
			t.Errorf("stdout = %q, want the app-build failure surfaced", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})

	refusals := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "a tier mismatch refuses without deploying",
			env:  map[string]string{clitest.FakeInfraTierEnvVar: "preview", clitest.FakeInfraPresentEnvVar: "1"},
			want: "this command needs production infrastructure",
		},
		{
			name: "absent infrastructure refuses without deploying",
			env:  map[string]string{clitest.FakeInfraPresentEnvVar: "0"},
			want: "ocel bootstrap",
		},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			root, _ := clitest.SetUpDeployFixture(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
			if err == nil {
				t.Fatal("runDeploy err = nil, want a refusal")
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.want)
			}
			if strings.Contains(stdout.String(), "DEPLOY ") {
				t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
			}
		})
	}

	t.Run("stdin that is not a terminal proceeds without prompting", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: false}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		if strings.Contains(stdout.String(), "[y/N]") {
			t.Errorf("stdout = %q, want the confirm prompt skipped for non-TTY stdin", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want deploy to still proceed to success", stdout.String())
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("declared domains pass the slug to the preflight", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { production: "app.acme.com" },
};
`)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug=test-app") {
			t.Errorf("stdout = %q, want the preflight to have included the project's slug", stdout.String())
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("--yes asks the provider exactly what the same run without it would", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "PREFLIGHT slug=test-app") {
			t.Errorf("stdout = %q, want --yes to leave the slug-scoped question on the wire untouched", out)
		}
		if !strings.Contains(out, "This will create a NEW project.") {
			t.Errorf("stdout = %q, want the drift warning still told to whoever passed --yes", out)
		}
		if strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to grant the guard rather than raise it", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a non-TTY stdin leaves the slug out", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: false}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PREFLIGHT slug= ") {
			t.Errorf("stdout = %q, want a non-TTY deploy to ask for no slug-scoped answers", stdout.String())
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("an interactive deploy warns about other projects", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "PREFLIGHT slug=test-app") {
			t.Errorf("stdout = %q, want the prompting deploy to have asked with the slug", out)
		}
		for _, want := range []string{
			"No existing deployment for slug \"test-app\".",
			"This will create a NEW project.",
			"This backend already has: my-application, billing",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("--yes bypasses the slug drift guard", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeKnownSlugsEnvVar, "my-application,billing")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if strings.Contains(out, "NEW project") || strings.Contains(out, "[y/N]") {
			t.Errorf("stdout = %q, want --yes to bypass the drift prompt", out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want the deploy to proceed", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("the identity banner prints before the build and the deploy", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		pretendStdoutIsTerminal(&dependencies)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeIDProviderEnvVar, "fake")
		t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
		t.Setenv(clitest.FakeIDLocationEnvVar, "zone-a")
		t.Setenv(clitest.FakeIDEdgeScopeEnvVar, "abcd1234")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := ansi.Strip(stdout.String())
		for _, want := range []string{"ocel", "test-app › production", "fake", "123456789012", "zone-a", "edge", "abcd1234"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q:\n%s", want, out)
			}
		}
		banner := strings.Index(out, "test-app › production")
		build := strings.Index(out, "[build]")
		deploy := strings.Index(out, "DEPLOY ")
		if banner < 0 || build < 0 || deploy < 0 {
			t.Fatalf("expected banner, build, and deploy all present; banner=%d build=%d deploy=%d\n%s", banner, build, deploy, out)
		}
		if banner >= build || build >= deploy {
			t.Errorf("expected order banner < build < deploy; got banner=%d build=%d deploy=%d\n%s", banner, build, deploy, out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("the identity banner prints with no terminal to print it to", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeIDProviderEnvVar, "fake")
		t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
		t.Setenv(clitest.FakeIDLocationEnvVar, "zone-a")
		t.Setenv(clitest.FakeIDEdgeScopeEnvVar, "abcd1234")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Deployed") {
			t.Fatalf("stdout = %q, want the deploy to have proceeded", out)
		}
		for _, want := range []string{"ocel  dev  test-app › production", "  fake  123456789012 · zone-a\n  edge  abcd1234"} {
			if !strings.Contains(out, want+"\n") {
				t.Errorf("stdout missing %q with no terminal attached:\n%s", want, out)
			}
		}
		if strings.Contains(out, "\x1b[") {
			t.Errorf("the banner painted colour with no terminal attached:\n%q", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a credential problem aborts before the build and the deploy", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		pretendStdoutIsTerminal(&dependencies)
		root, _ := clitest.SetUpDeployFixture(t)
		t.Setenv(clitest.FakeIDAccountEnvVar, "123456789012")
		t.Setenv(clitest.FakeIDProfileEnvVar, "default")
		t.Setenv(clitest.FakeCredProblemEnvVar, "Relay")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a credential-check error")
		}

		out := stdout.String()
		if !strings.Contains(out, "123456789012") {
			t.Errorf("stdout = %q, want the resolved identity still shown", out)
		}
		if !strings.Contains(out, "Relay") {
			t.Errorf("stdout = %q, want the Relay credential problem surfaced", out)
		}
		if strings.Contains(out, "[build]") {
			t.Errorf("stdout = %q, want the build to be skipped on a credential failure", out)
		}
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a single app produces exactly one attributed app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  apps: [{ name: "api", path: "apps/api", framework: "node", domains: { production: "Api.Acme.com" } }],
};
`)
		writeAppSource(t, root, "api")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "APP "); got != 1 {
			t.Fatalf("stdout echoed %d apps, want exactly 1:\n%s", got, out)
		}
		if !strings.Contains(out, "APP name=api framework=node production_domain=api.acme.com") {
			t.Errorf("stdout = %q, want the app with its per-app production domain", out)
		}
		if !strings.Contains(out, "deployment="+clitest.FixtureDeploymentID("api")) {
			t.Errorf("stdout = %q, want the app deployed under the id its build recorded", out)
		}
		if !strings.Contains(out, "framework=node entry_file=src/server.js artifact_path=output/api app=api") {
			t.Errorf("stdout = %q, want the function attributed to the api app", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("two apps attribute their functions to their own app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "web", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/web", App: "web"},
			{Route: "admin", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/admin", App: "admin"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  apps: [
    { name: "web", path: "apps/web", framework: "node", domains: { production: "acme.com" } },
    { name: "admin", path: "apps/admin", framework: "node" },
  ],
};
`)
		writeAppSource(t, root, "web", "admin")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "INFO  [build] ✓ test-app: Collected the resources test-app declares in ") {
			t.Errorf("stdout = %q, want the build phase to say whose resources it collected", out)
		}
		if got := strings.Count(out, "APP "); got != 2 {
			t.Fatalf("stdout echoed %d apps, want exactly 2:\n%s", got, out)
		}
		if !strings.Contains(out, "APP name=admin framework=node production_domain=") {
			t.Errorf("stdout = %q, want the admin app with no domain of its own", out)
		}
		if !strings.Contains(out, "APP name=web framework=node production_domain=acme.com") {
			t.Errorf("stdout = %q, want the web app with its own production domain", out)
		}
		if !strings.Contains(out, "logical_name=fn--web--web") || !strings.Contains(out, "artifact_path=output/web app=web") {
			t.Errorf("stdout = %q, want the web function attributed to the web app", out)
		}
		if !strings.Contains(out, "logical_name=fn--admin--admin") || !strings.Contains(out, "artifact_path=output/admin app=admin") {
			t.Errorf("stdout = %q, want the admin function attributed to the admin app", out)
		}
		for _, app := range []string{"web", "admin"} {
			if !strings.Contains(out, "name="+app+" framework=node production_domain=") {
				t.Errorf("stdout = %q, want %s echoed", out, app)
			}
			if !strings.Contains(out, "deployment="+clitest.FixtureDeploymentID(app)) {
				t.Errorf("stdout = %q, want %s deployed under the id its own build recorded", out, app)
			}
		}
		if clitest.FixtureDeploymentID("web") == clitest.FixtureDeploymentID("admin") {
			t.Fatal("the fixture gives both apps one id, so this proves nothing")
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("the app at the root of a project naming none appears in the manifest under its slug", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "h.js", ArtifactPath: "output/index", App: clitest.FixtureSlug},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		writeRootApp(t, root)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if got := strings.Count(out, "APP "); got != 1 {
			t.Fatalf("stdout echoed %d apps, want exactly 1:\n%s", got, out)
		}
		if !strings.Contains(out, "APP name="+clitest.FixtureSlug+" framework=node production_domain=") {
			t.Errorf("stdout = %q, want the root app named after the slug in the manifest", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})
}

func pretendStdoutIsTerminal(dependencies *Dependencies) {
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{TTY: true})
	}
}

func addAppToFixtureConfig(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: { location: "zone-b" } },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)
	writeAppSource(t, root, "api")
}

func writeAppSource(t *testing.T, root string, apps ...string) {
	t.Helper()
	for _, app := range apps {
		clitest.WriteFile(t, filepath.Join(root, "apps", app, "src", "server.ts"), `
export function handler() {
  return "`+app+`";
}
`)
	}
}

func TestRunDeployRefusesAComputeTheProviderDoesNotRun(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	root, sockPath := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "container" }],
};
`)
	writeAppSource(t, root, "api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{`"api"`, `"container"`, "fake", "serverless"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want the refusal to name %s", out, want)
		}
	}

	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestADeploysResultNamesTheProjectAndProduction(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	evs := envelopes(t, stream.String())
	if headline := evs[len(evs)-1].GetSummary().GetHeadline(); headline != "Deployed test-app to production" {
		t.Fatalf("result headline = %q, want it to name the project and production", headline)
	}
}

func twoAppFixture(t *testing.T) (Dependencies, string) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: { location: "zone-b" } },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
};
`)
	writeAppSource(t, root, "web", "api")
	return dependencies, root
}

func buildingEach(failing string) func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
	return func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, out build.Log) (build.Output, error) {
		_, _ = io.WriteString(out.Shared, "the builder started\n")
		for _, app := range cfg.Apps {
			log, ended := out.App(app.Name)
			_, _ = fmt.Fprintf(log, "compiling %s\n", app.Name)
			if app.Name == failing {
				err := errors.New(app.Name + " did not compile")
				ended(err)
				return build.Output{}, err
			}
			ended(nil)
		}
		return build.Output{}, nil
	}
}

type buildScope struct {
	subject, message string
	started, ended   int
	status           progressv1.SpanStatus
	output           []string
}

func buildScopes(t *testing.T, stream string) ([]*buildScope, []string) {
	t.Helper()
	var scopes []*buildScope
	bySpan := map[string]*buildScope{}
	var phaseOutput []string
	for i, ev := range envelopes(t, stream) {
		if ev.GetPhase() != progressv1.Phase_PHASE_BUILD {
			continue
		}
		span := string(ev.GetSpanId())
		switch body := ev.GetBody().(type) {
		case *streamv1.RunEvent_Started:
			if ev.GetSubject() == "" || ev.GetLevel() == progressv1.Level_LEVEL_DEBUG {
				continue
			}
			scope := &buildScope{subject: ev.GetSubject(), message: ev.GetMessage(), started: i}
			bySpan[span] = scope
			scopes = append(scopes, scope)
		case *streamv1.RunEvent_Ended:
			if scope, ok := bySpan[span]; ok {
				scope.ended, scope.status = i, body.Ended.GetStatus()
			}
		case *streamv1.RunEvent_Output:
			if scope, ok := bySpan[span]; ok {
				scope.output = append(scope.output, ev.GetMessage())
			} else {
				phaseOutput = append(phaseOutput, ev.GetMessage())
			}
		}
	}
	return scopes, phaseOutput
}

func TestEachAppBuildsAsAUnitOfItsOwnInTheBuildPhaseOnceTheDeclarationsAreCollected(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}

	scopes, phaseOutput := buildScopes(t, stdout.String())
	var got []string
	for _, scope := range scopes {
		got = append(got, scope.subject+": "+scope.message)
	}
	want := []string{
		clitest.FixtureSlug + ": Collecting the resources " + clitest.FixtureSlug + " declares",
		"web: Building app web",
		"api: Building app api",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("build units =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, scope := range scopes {
		if scope.status != progressv1.SpanStatus_SPAN_STATUS_OK {
			t.Errorf("%s ended %s, want OK", scope.subject, scope.status)
		}
		if i > 0 && scopes[i-1].ended > scope.started {
			t.Errorf("%s started before %s ended, want each unit to end before the next begins", scope.subject, scopes[i-1].subject)
		}
	}
	for _, scope := range scopes[1:] {
		if want := []string{"compiling " + scope.subject}; strings.Join(scope.output, "\n") != want[0] {
			t.Errorf("%s's output = %q, want %q", scope.subject, scope.output, want)
		}
	}
	if strings.Join(phaseOutput, "\n") != "the builder started" {
		t.Errorf("build phase output = %q, want what no app's build said and nothing else", phaseOutput)
	}
}

func TestAnAppWhoseBuildFailsEndsItsOwnUnitInFailureAndTheDeployWithIt(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want api's build failure")
	}

	scopes, _ := buildScopes(t, stdout.String())
	statuses := map[string]progressv1.SpanStatus{}
	for _, scope := range scopes {
		statuses[scope.subject] = scope.status
	}
	if statuses["web"] != progressv1.SpanStatus_SPAN_STATUS_OK || statuses["api"] != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("unit statuses = %v, want web OK and api ERROR", statuses)
	}
	if statuses[clitest.FixtureSlug] != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the collecting unit ended %s, want OK: it finished before any app built", statuses[clitest.FixtureSlug])
	}
}

func TestEachAppsBuildPrintsAsABlockOfItsOwnWhenThatAppFinishes(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.Presentation = func(io.Writer) terminal.Presentation { return terminal.Resolve(terminal.Conditions{}) }
	dependencies.BuildApps = buildingEach("")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}

	out := stdout.String()
	for _, app := range []string{"web", "api"} {
		block := "INFO  [build] ✓ " + app + ": Built app " + app + " in <1s\n\n    compiling " + app + "\n"
		if !strings.Contains(out, block) {
			t.Errorf("stdout = %q, want %s's block %q", out, app, block)
		}
	}
	if web, api := strings.Index(out, "✓ web: Built"), strings.Index(out, "✓ api: Built"); web < 0 || api < web {
		t.Errorf("stdout = %q, want web's block before api's, in the order they finished", out)
	}
}

func TestABuilderFailureOutsideEveryAppsBuildEndsAUnitOfItsOwnHoldingWhatTheBuilderSaid(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, _ map[string]map[string]string, _ map[string]string, out build.Log) (build.Output, error) {
		_, _ = io.WriteString(out.Shared, "Error: Cannot find module 'esbuild'\n")
		return build.Output{}, errors.New("node-builder failed (exit status 1): Error: Cannot find module 'esbuild'")
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want the builder's failure")
	}

	scopes, phaseOutput := buildScopes(t, stdout.String())
	last := scopes[len(scopes)-1]
	if last.subject != clitest.FixtureSlug || last.message != "Building 2 apps (web and api)" || last.status != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("the last build unit = %s: %q ended %s, want %s: \"Building 2 apps (web and api)\" ended in error", last.subject, last.message, last.status, clitest.FixtureSlug)
	}
	if strings.Join(last.output, "\n") != "Error: Cannot find module 'esbuild'" {
		t.Errorf("the failed unit's output = %q, want what the builder said", last.output)
	}
	if len(phaseOutput) != 0 {
		t.Errorf("build phase output = %q, want none: the builder's words belong to the unit that failed", phaseOutput)
	}
}

func TestAnAppsOwnBuildFailureEndsNoSecondUnit(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("web")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want web's build failure")
	}

	scopes, phaseOutput := buildScopes(t, stdout.String())
	var got []string
	for _, scope := range scopes {
		got = append(got, scope.subject+" "+scope.status.String())
	}
	want := []string{clitest.FixtureSlug + " SPAN_STATUS_OK", "web SPAN_STATUS_ERROR"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("build units = %q, want %q: web's unit already reports the failure", got, want)
	}
	if strings.Join(phaseOutput, "\n") != "the builder started" {
		t.Errorf("build phase output = %q, want what the builder said before web's build", phaseOutput)
	}
}

func TestAFailureAssemblingTheManifestAfterTheBuildsEndsAUnitOfItsOwn(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("")
	dependencies.DeploymentID = func(string, string) (string, error) {
		return "", errors.New("no deployment id for app \"web\"; run `ocel build`")
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want the manifest's failure")
	}

	scopes, _ := buildScopes(t, stdout.String())
	last := scopes[len(scopes)-1]
	if last.subject != clitest.FixtureSlug || last.message != "Assembling the deploy manifest of "+clitest.FixtureSlug || last.status != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("the last build unit = %s: %q ended %s, want the manifest's unit ended in error", last.subject, last.message, last.status)
	}
}

func writeBoundMonorepo(t *testing.T, root string, bindings string) {
	t.Helper()

	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  bindings: {`+bindings+`},
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)
}

func deployBound(t *testing.T, bindings string) (root string, stdout, stderr bytes.Buffer, err error) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	root, _ = clitest.SetUpDeployFixture(t)
	writeBoundMonorepo(t, root, bindings)

	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err = runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	return root, stdout, stderr, err
}

func TestDeployBindsListedBindings(t *testing.T) {
	t.Run("a listed resource reaches the provider bound to its published record", func(t *testing.T) {
		t.Setenv(clitest.FakePublishedBindingsEnvVar, "main")

		_, stdout, stderr, err := deployBound(t, `postgres: { main: "@main" }`)
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "BINDING bound=db--main name=main") {
			t.Errorf("stdout = %q, want the `bindings` binding to have reached the provider on the manifest", out)
		}
		if !strings.Contains(out, "USAGE app=api resource=db--main") {
			t.Errorf("stdout = %q, want a bound resource to have its usage edge like any other", out)
		}
	})

	t.Run("a listed resource nothing published refuses the deploy by name", func(t *testing.T) {
		t.Setenv(clitest.FakePublishedBindingsEnvVar, "")

		_, stdout, stderr, err := deployBound(t, `postgres: { main: "@main" }`)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "published a binding named main") {
			t.Errorf("output = %q, want the refusal to name the binding that was never published", combined)
		}
	})

	t.Run("a published name this project provisions instead is called out", func(t *testing.T) {
		t.Setenv(clitest.FakePublishedBindingsEnvVar, "main")

		_, stdout, stderr, err := deployBound(t, ``)
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "BINDING shadowed=db--main name=main") {
			t.Errorf("stdout = %q, want the collision between a provisioned resource and a published binding surfaced", out)
		}
	})

	t.Run("a listed name nothing declares refuses before any provider is reached", func(t *testing.T) {
		_, stdout, stderr, err := deployBound(t, `postgres: { nowhere: "@nowhere" }`)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "nowhere") {
			t.Errorf("output = %q, want the unbound binding named", combined)
		}
	})
}

const infisicalProduction = `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },
  },
};
`

const stripeDeclared = `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true,"description":"Stripe's secret key"}]`

func setUpInfisicalFixture(t *testing.T, config string, source clitest.FakeEnvSource) string {
	t.Helper()
	root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), config)
	envSet(t, root, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
	envSet(t, root, "INFISICAL_CLIENT_SECRET", "client-secret", envOptions{})
	useFakeEnvSource(t, source)
	return root
}

func useFakeEnvSource(t *testing.T, source clitest.FakeEnvSource) {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clitest.FakeEnvSourceEnvVar, string(raw))
}

var infisicalURLs = map[string]string{"": "https://infisical.example/p-1/prod"}

func TestADeployReadsItsTiersEnvSourceBeforeCheckingItsVariables(t *testing.T) {
	t.Run("a value the env source has passes the variables check", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk_live_from_infisical"}},
			URLs:   infisicalURLs,
		})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want the deploy to complete on the env source's value", stdout.String())
		}
	})

	t.Run("a value the env source lacks refuses, naming where to set it", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{URLs: infisicalURLs})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the variables check to refuse")
		}
		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "set it in infisical:p-1/prod", "https://infisical.example/p-1/prod"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want %q", out, want)
			}
		}
		if strings.Contains(out, "ocel env set STRIPE_API_KEY") {
			t.Errorf("stdout = %q, want no `ocel env set` offered for a value the env source owns", out)
		}
	})

	t.Run("what the env source has and nothing declares is a warning, not a refusal", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}, {Key: "OLD_TOKEN", Value: "old"}},
		})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		if out := stdout.String(); !strings.Contains(out, "OLD_TOKEN") || !strings.Contains(out, "nothing this project declares") {
			t.Errorf("stdout = %q, want OLD_TOKEN reported as undeclared", out)
		}
	})

	t.Run("an env source that cannot be read stops the deploy before anything is built", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{ReadError: "Infisical answered 503"})
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || built {
			t.Fatalf("runDeploy err = %v, built = %v, want an unreadable env source to stop the deploy first", err, built)
		}
		if out := stdout.String() + err.Error(); !strings.Contains(out, "503") {
			t.Errorf("output = %q, want the env source's failure named", out)
		}
	})

	t.Run("an unset credential stops the deploy with the command that sets it", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		envSet(t, root, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
		useFakeEnvSource(t, clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		out := stdout.String()
		if err == nil || !strings.Contains(out, "1 variable is not ready") || !strings.Contains(out, "INFISICAL_CLIENT_SECRET  root  no value") {
			t.Fatalf("runDeploy err = %v; stdout=%s, want the variables check to refuse the unset credential alone", err, out)
		}
		if !strings.Contains(out, "ocel env set INFISICAL_CLIENT_SECRET=<VALUE>") {
			t.Errorf("stdout = %q, want the credential's command named, not the env source", out)
		}
	})

	t.Run("an unset credential opens the recovery page, and saving it there resumes the deploy", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), infisicalProduction)
		useFakeEnvSource(t, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk_live_from_infisical"}},
		})
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)

		var out syncBuffer
		var stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &out)
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		setCell(t, address, token, "INFISICAL_CLIENT_ID", "client-id")
		setCell(t, address, token, "INFISICAL_CLIENT_SECRET", "client-secret")
		markDone(t, address, token)

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runDeploy err = %v, want the deploy to resume on the env source's values; stdout=%s", err, out.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the credentials were saved")
		}
		if !strings.Contains(out.String(), "Deployed") {
			t.Errorf("stdout = %q, want the resumed deploy to have completed", out.String())
		}
	})

	t.Run("a writable env source is handed the keys it lacks, empty, for a human to fill", func(t *testing.T) {
		root := setUpInfisicalFixture(t, strings.Replace(infisicalProduction, `environment: "prod",`, `environment: "prod", write: "missing",`, 1), clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
			t.Fatal("runDeploy err = nil, want the variables check to refuse: an empty key is still unset")
		}
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		created := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)].Created
		if len(created) != 1 || created[0].Key != "STRIPE_API_KEY" || created[0].Value != "" {
			t.Fatalf("created = %+v, want STRIPE_API_KEY created empty", created)
		}
		if out := stdout.String(); !strings.Contains(out, "Created STRIPE_API_KEY empty in infisical:p-1/prod") {
			t.Errorf("stdout = %q, want the creation reported", out)
		}
	})

	t.Run("a dry run creates nothing in the env source", func(t *testing.T) {
		root := setUpInfisicalFixture(t, strings.Replace(infisicalProduction, `environment: "prod",`, `environment: "prod", write: "missing",`, 1), clitest.FakeEnvSource{})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		_ = runDeploy(context.Background(), dependencies, root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader(""))
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		registration, registered := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)]
		if !registered || !strings.Contains(stdout.String(), "STRIPE_API_KEY") {
			t.Fatalf("registered = %v, stdout = %q, want the dry run to have read the env source and refused", registered, stdout.String())
		}
		if len(registration.Created) != 0 {
			t.Errorf("created = %+v, want a dry run to write nothing into the env source", registration.Created)
		}
	})

	t.Run("exec runs where ocel deploys, and the provider is handed what it printed", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), strings.Replace(infisicalProduction,
			`production: { infisical: { project: "p-1", environment: "prod", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },`,
			`production: { exec: { command: ["sh", "-c", "printf 'STRIPE_API_KEY=sk_from_exec'"], format: "dotenv" } },`, 1))

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		store, err := clitest.LoadFakeStore()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, cell := range store {
			latest := cell.Versions[len(cell.Versions)-1]
			found = found || (cell.Coordinate.Key == "STRIPE_API_KEY" && latest.Value == "sk_from_exec" && latest.EnvSource == "exec")
		}
		if !found {
			t.Errorf("store = %+v, want STRIPE_API_KEY copied from exec", store)
		}
	})

	t.Run("a tier back on builtin forgets the env source it read before", func(t *testing.T) {
		root := setUpInfisicalFixture(t, infisicalProduction, clitest.FakeEnvSource{
			Values: []clitest.FakeEnvSourceValue{{Key: "STRIPE_API_KEY", Value: "sk"}},
		})
		var first, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &first)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &first, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, first.String())
		}
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)

		var second bytes.Buffer
		dependencies = newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &second)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &second, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, second.String())
		}
		registrations, err := clitest.LoadFakeRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		if _, registered := registrations[clitest.FakeRegistrationKey(environmentv1.Tier_TIER_PRODUCTION, clitest.FixtureSlug)]; registered {
			t.Errorf("registrations = %+v, want production's registration forgotten", registrations)
		}
	})
}

const inlineURL = "postgres://app:s3cret-pw@ep-cool.neon.tech/main?sslmode=require"

const inlineByURL = `postgres: { main: { url: { $env: "MAIN_DATABASE_URL" } } }`

type inlineRun struct {
	root    string
	journal string
}

func setUpInline(t *testing.T, bindings string) inlineRun {
	t.Helper()
	root, _ := clitest.SetUpDeployFixture(t)
	writeBoundMonorepo(t, root, bindings)
	t.Setenv(clitest.FakeVarsStoreEnvVar, filepath.Join(t.TempDir(), "vars.json"))
	t.Setenv(clitest.FakeBindingsStoreEnvVar, filepath.Join(t.TempDir(), "bindings.json"))
	journal := filepath.Join(t.TempDir(), "deploy.json")
	t.Setenv(clitest.FakeDeployJournalEnvVar, journal)
	return inlineRun{root: root, journal: journal}
}

func seedProduction(t *testing.T, key, value string) {
	t.Helper()
	store, err := clitest.LoadFakeStore()
	if err != nil {
		t.Fatal(err)
	}
	c := &envvarsv1.Coordinate{Slug: clitest.FixtureSlug, Key: key}
	store[clitest.FakeCoordinateID(environmentv1.Tier_TIER_PRODUCTION, c)] = &clitest.FakeCell{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: clitest.FakeCoordinate{Slug: clitest.FixtureSlug, Key: key},
		Versions:   []clitest.FakeCellData{{Value: value, Ts: 1_700_000_000}},
	}
	if err := clitest.SaveFakeStore(store); err != nil {
		t.Fatal(err)
	}
}

func (r inlineRun) deploy(t *testing.T, opts deployOptions) (string, error) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	opts.yes = true
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, r.root, opts, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func sentRequest(t *testing.T, journal string) *contractv1.DeployRequest {
	t.Helper()
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("read the deploy request: %v", err)
	}
	req := &contractv1.DeployRequest{}
	if err := protojson.Unmarshal(raw, req); err != nil {
		t.Fatalf("decode the deploy request: %v", err)
	}
	return req
}

func carriedNames(carried []*bindingsv1.Binding) []string {
	names := make([]string, 0, len(carried))
	for _, binding := range carried {
		names = append(names, binding.GetName()+" from "+binding.GetSource())
	}
	return names
}

func TestDeployBindsAnInlineRecord(t *testing.T) {
	t.Run("the request carries the record whole and the manifest names it, never its secret", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)

		out, err := run.deploy(t, deployOptions{})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		if !strings.Contains(out, "BINDING bound=db--main name=main record=ocel:postgres.main carried") {
			t.Errorf("output = %q, want main bound to the record the request carries", out)
		}
		req := sentRequest(t, run.journal)
		carried := req.GetInlineBindings()
		if len(carried) != 1 || carried[0].GetName() != "ocel:postgres.main" || carried[0].GetSource() != "ocel.config.ts" {
			t.Fatalf("inline bindings = %v, want the one record, sourced from the config", carriedNames(carried))
		}
		if carried[0].GetPostgres().GetUrl() != inlineURL {
			t.Error("the carried record lost the url the app connects with")
		}
		manifest, err := protojson.Marshal(req.GetManifest())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(manifest), "s3cret-pw") {
			t.Errorf("the manifest contains the database password: %s", manifest)
		}
		if strings.Contains(out, "s3cret-pw") {
			t.Errorf("the deploy printed the database password: %s", out)
		}
	})

	t.Run("a variable the binding reads and nobody set refuses the deploy with the command that sets it", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)

		out, err := run.deploy(t, deployOptions{})
		if err == nil {
			t.Fatalf("deploy succeeded with MAIN_DATABASE_URL unset\n%s", out)
		}
		if said := err.Error() + out; !strings.Contains(said, "ocel env set MAIN_DATABASE_URL=<VALUE>") {
			t.Errorf("refusal = %q, want the command that sets it", said)
		}
		if _, statErr := os.Stat(run.journal); statErr == nil {
			t.Error("the deploy reached the provider without the record's value")
		}
	})

	t.Run("a dry run hands the provider the record to plan with", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)

		if out, err := run.deploy(t, deployOptions{dry: true}); err != nil {
			t.Fatalf("dry deploy: %v\n%s", err, out)
		}
		req := sentRequest(t, run.journal)
		if !req.GetDry() || len(req.GetInlineBindings()) != 1 {
			t.Errorf("request dry = %v with %d inline bindings, want the dry plan to carry the record", req.GetDry(), len(req.GetInlineBindings()))
		}
	})

	t.Run("dropping the binding leaves the request carrying no record", func(t *testing.T) {
		run := setUpInline(t, inlineByURL)
		seedProduction(t, "MAIN_DATABASE_URL", inlineURL)
		if out, err := run.deploy(t, deployOptions{}); err != nil {
			t.Fatalf("first deploy: %v\n%s", err, out)
		}

		writeBoundMonorepo(t, run.root, ``)
		if out, err := run.deploy(t, deployOptions{}); err != nil {
			t.Fatalf("second deploy: %v\n%s", err, out)
		}
		if carried := sentRequest(t, run.journal).GetInlineBindings(); len(carried) != 0 {
			t.Errorf("inline bindings = %v, want none: the provider prunes what the request no longer carries", carriedNames(carried))
		}
	})
}

const inlineBucket = `bucket: { uploads: {
  endpoint: "https://abc.storage.example.com", region: "auto", bucket: "acme", prefix: "uploads/",
  accessKeyId: { $env: "R2_KEY" }, secretAccessKey: { $env: "BUCKET_SECRET" },
} }`

func declareUploads(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const files = declareBucket("uploads");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, files } from "../../../shared/index.js";

export function handler() {
  return db.name + files.name;
}
`)
}

func TestDeployBindsAnInlineBucket(t *testing.T) {
	run := setUpInline(t, inlineBucket)
	declareUploads(t, run.root)
	seedProduction(t, "R2_KEY", "AKIDEXAMPLE")
	seedProduction(t, "BUCKET_SECRET", "bucket-s3cret")

	out, err := run.deploy(t, deployOptions{})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	carried := sentRequest(t, run.journal).GetInlineBindings()
	if len(carried) != 1 || carried[0].GetName() != "ocel:bucket.uploads" {
		t.Fatalf("inline bindings = %v, want the inline bucket's record", carriedNames(carried))
	}
	want := &bindingsv1.BucketProperties{
		Endpoint: "https://abc.storage.example.com", Region: "auto", Bucket: "acme", Prefix: "uploads/",
		AccessKeyId: "AKIDEXAMPLE", SecretAccessKey: "bucket-s3cret",
	}
	if got := carried[0].GetBucket(); !proto.Equal(got, want) {
		t.Errorf("bucket = %s at %s under %q, want %s at %s under %q with the key pair the variables hold",
			got.GetBucket(), got.GetEndpoint(), got.GetPrefix(), want.GetBucket(), want.GetEndpoint(), want.GetPrefix())
	}
	if strings.Contains(out, "bucket-s3cret") {
		t.Errorf("the deploy printed the store's secret key: %s", out)
	}
}

func stubAppBuildRecorder(dependencies *Dependencies, built *bool) {
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		*built = true
		return functionsOnDisk(cfg)
	}
}

func writeRootApp(t *testing.T, root string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "package.json"), "{}\n")
}

func captureBuildEnv(dependencies *Dependencies) *map[string]map[string]string {
	stubRecordedDeploymentIDs(dependencies)
	var got map[string]map[string]string
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, envByApp map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		got = envByApp
		return functionsOnDisk(cfg)
	}
	return &got
}

func writeAppsConfig(t *testing.T, root, apps string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  apps: [`+apps+`],
};
`)
}

func TestADeployIsRefusedUntilItsVariablesAreReady(t *testing.T) {
	t.Run("a missing value refuses before anything is built", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		t.Setenv("OCEL_TEST_ENV_PROBLEMS", `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`)
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the declarations to refuse")
		}
		var exit *exitcode.ExitError
		if !errors.As(err, &exit) || exit.Code == 0 {
			t.Errorf("runDeploy err = %v, want a non-zero exit", err)
		}

		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY=<VALUE>"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if built {
			t.Error("the app was built, want the declarations to refuse before any build runs")
		}
		if strings.Contains(out, "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out)
		}
	})

	t.Run("a client value that fails its schema refuses before anything is built, naming the key and the complaint", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"NEXT_PUBLIC_PORT","class":"VARIABLE_CLASS_PLAIN","required":true,"clientAccessible":true,"hasSchema":true,"schemaSource":"/app/env.schema.ts","source":"/app/env.ts"}]`)
		t.Setenv("OCEL_TEST_ENV_PROBLEMS", `[{"key":"NEXT_PUBLIC_PORT","folder":"","kind":"KIND_INVALID","detail":"expected a number"}]`)
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the declarations to refuse")
		}
		out := stdout.String()
		for _, want := range []string{"NEXT_PUBLIC_PORT", "set, but expected a number", "ocel env set NEXT_PUBLIC_PORT=<VALUE>"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if built {
			t.Error("the app was built with a client value its schema rejects")
		}
	})

	t.Run("a missing value refuses though discovery reported nothing", func(t *testing.T) {
		root := clitest.SetUpVariablesFixtureWith(t,
			`[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`,
			clitest.EnvDeclareOnlyScript)
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the declarations to refuse on what it knows itself")
		}
		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY=<VALUE>"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if built {
			t.Error("the app was built, want the declarations to refuse before any build runs")
		}
	})

	t.Run("a value that is set passes the declarations and deploys", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_value", envOptions{})

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Deployed") {
			t.Errorf("stdout = %q, want the deploy to have completed", stdout.String())
		}
	})

	t.Run("a value that cannot be read names the cell", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_value", envOptions{})
		t.Setenv(clitest.FakeRevealFailureEnvVar, "the store is unreachable")

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a store it cannot read to stop the deploy")
		}

		out := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(out, "STRIPE_API_KEY (project root)") {
			t.Errorf("output = %q, want it to name the cell that could not be read", out)
		}
	})

	t.Run("a live value is never handed to the declaring process", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true},{"key":"BAKED_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		envSet(t, root, "LIVE_KEY", "sk_live_do_not_leak", envOptions{})
		envSet(t, root, "BAKED_KEY", "baked_value", envOptions{})

		cellsPath := filepath.Join(t.TempDir(), "cells.json")
		t.Setenv("OCEL_TEST_ENV_CELLS_OUT", cellsPath)

		var stdout, stderr bytes.Buffer
		dependencies := newTestDependencies()
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		raw, readErr := os.ReadFile(cellsPath)
		if readErr != nil {
			t.Fatalf("read cells handed to discovery: %v", readErr)
		}
		if strings.Contains(string(raw), "sk_live_do_not_leak") {
			t.Errorf("cells = %s, want a live value never pulled onto the build host", raw)
		}

		var cells []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &cells); err != nil {
			t.Fatalf("unmarshal cells: %v", err)
		}
		byKey := map[string]string{}
		for _, c := range cells {
			byKey[c.Key] = c.Value
		}
		if _, ok := byKey["LIVE_KEY"]; !ok {
			t.Error("cells has no LIVE_KEY, want the live cell reported present so it is not called missing")
		}
		if byKey["BAKED_KEY"] != "baked_value" {
			t.Errorf("BAKED_KEY = %q, want the plaintext its schema is checked against", byKey["BAKED_KEY"])
		}
	})

	t.Run("a folder no app binds is a warning, not a refusal", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)
		writeAppsConfig(t, root, `{ name: "api", path: "apps/api", framework: "node" }`)
		writeAppSource(t, root, "api")
		envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"})
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v, want a dead scope to warn, not stop the deploy; stdout=%s", err, stdout.String())
		}
		out := stdout.String() + stderr.String()
		if !strings.Contains(out, "POSTHOG_ID") || !strings.Contains(out, "/web") {
			t.Errorf("output = %q, want a warning naming the key and the folder no app binds", out)
		}
	})

	t.Run("each app is built with its own diverged value", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"]}]`)
		writeAppsConfig(t, root, `
    { name: "web", path: "apps/web", framework: "node", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "node", folder: "/admin" }`)
		writeAppSource(t, root, "web", "admin")
		envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"})
		envSet(t, root, "POSTHOG_ID", "ph_admin", envOptions{folder: "/admin"})

		dependencies := newTestDependencies()
		got := captureBuildEnv(&dependencies)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
		}
		if (*got)["web"]["POSTHOG_ID"] != "ph_web" || (*got)["admin"]["POSTHOG_ID"] != "ph_admin" {
			t.Errorf("build environments = %v, want each app the value it resolved", *got)
		}
	})

	t.Run("a half-completed folder rename stops the deploy naming both files", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"],"source":"ocel/env.ts"}]`)
		writeAppsConfig(t, root, `
    { name: "web", path: "apps/web", framework: "node", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "node", folder: "/administration" }`)
		envSet(t, root, "POSTHOG_ID", "ph_web", envOptions{folder: "/web"})
		envSet(t, root, "POSTHOG_ID", "ph_admin", envOptions{folder: "/admin"})

		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a half-finished folder rename to stop the deploy")
		}
		out := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{"POSTHOG_ID", "/admin", "ocel.config.ts", "env.ts"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want it to name %q", out, want)
			}
		}
		if built {
			t.Error("the app was built, want the lint to refuse before any build runs")
		}
	})

	t.Run("a reference satisfies the declarations with its source's value", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		ownedElsewhere(t, "POSTHOG_ID", "ph_owned_by_platform")
		envRef(t, root, "POSTHOG_ID", envOptions{}, envRefOptions{project: "platform"})
		writeRootApp(t, root)

		dependencies := newTestDependencies()
		got := captureBuildEnv(&dependencies)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if (*got)[clitest.FixtureSlug]["POSTHOG_ID"] != "ph_owned_by_platform" {
			t.Errorf("build environment = %v, want the value the other project stores", *got)
		}
	})
}

func TestAPreviewIsRefusedUntilItsVariablesAreReady(t *testing.T) {
	t.Run("a production value does not satisfy the preview declarations", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want the preview declarations to refuse: the production store is not the preview one")
		}

		out := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(out, "STRIPE_API_KEY") {
			t.Errorf("output = %q, want it to name the cell the preview bootstrap is missing", out)
		}
		if strings.Contains(out, "sk_live_secret") {
			t.Errorf("output = %q, want no production value reachable from a preview", out)
		}
		if built {
			t.Error("the app was built, want the declarations to refuse before any build runs")
		}
	})

	t.Run("the environment being deployed resolves its own override", func(t *testing.T) {
		for name, tc := range map[string]struct {
			deploying string
			want      string
		}{
			"the environment with the override": {deploying: "staging", want: "ph_staging"},
			"another preview":                   {deploying: "canary", want: "ph_shared"},
		} {
			t.Run(name, func(t *testing.T) {
				root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
				writeRootApp(t, root)
				dependencies := newTestDependencies()
				stubGit(&dependencies, "feature/login", "")
				t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
				t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

				envSet(t, root, "POSTHOG_ID", "ph_shared", envOptions{preview: true})
				envSet(t, root, "POSTHOG_ID", "ph_staging", envOptions{preview: true, environment: "staging"})

				got := captureBuildEnv(&dependencies)

				var stdout, stderr bytes.Buffer
				clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
				if err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: tc.deploying}, &stdout, &stderr, strings.NewReader("")); err != nil {
					t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
				}
				for app, env := range *got {
					if env["POSTHOG_ID"] != tc.want {
						t.Errorf("%s built with POSTHOG_ID=%q, want %q", app, env["POSTHOG_ID"], tc.want)
					}
				}
				if len(*got) == 0 {
					t.Fatal("no app was built, so nothing resolved a value")
				}
			})
		}
	})

	t.Run("an override is the only value its own environment needs", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		writeRootApp(t, root)
		dependencies := newTestDependencies()
		stubGit(&dependencies, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		envSet(t, root, "POSTHOG_ID", "ph_staging", envOptions{preview: true, environment: "staging"})

		got := captureBuildEnv(&dependencies)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v, want staging's own override to satisfy the declarations; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if len(*got) == 0 {
			t.Fatal("no app was built, so nothing resolved a value")
		}
		for app, env := range *got {
			if env["POSTHOG_ID"] != "ph_staging" {
				t.Errorf("%s built with POSTHOG_ID=%q, want %q", app, env["POSTHOG_ID"], "ph_staging")
			}
		}
	})

	t.Run("a redeployed branch finds the override it already had", func(t *testing.T) {
		root := clitest.SetUpVariablesFixture(t, `[{"key":"POSTHOG_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		writeRootApp(t, root)
		dependencies := newTestDependencies()
		stubGit(&dependencies, "feature/login", "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		envSet(t, root, "POSTHOG_ID", "ph_shared", envOptions{preview: true})
		envSet(t, root, "POSTHOG_ID", "ph_staging", envOptions{preview: true, environment: "staging"})

		got := captureBuildEnv(&dependencies)

		up := func(when string) {
			t.Helper()
			*got = nil
			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runPreviewUp %s err = %v; stdout=%s stderr=%s", when, err, stdout.String(), stderr.String())
			}
			if len(*got) == 0 {
				t.Fatalf("no app was built %s, so nothing resolved a value", when)
			}
			for app, env := range *got {
				if env["POSTHOG_ID"] != "ph_staging" {
					t.Errorf("%s built %s with POSTHOG_ID=%q, want %q", when, app, env["POSTHOG_ID"], "ph_staging")
				}
			}
		}

		up("before the teardown")

		var rm bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &rm)
		if err := runPreviewRemove(context.Background(), dependencies, root, previewRemoveOptions{name: "staging", yes: true}, &rm, &rm, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRemove err = %v; out=%s", err, rm.String())
		}

		up("after the branch was rebuilt")
	})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return string(encoded)
}

func setUpProviderFixture(t *testing.T, options string) (root, journal string, dependencies Dependencies) {
	t.Helper()
	return setUpProviderFixtureWith(t, options, nil)
}

func setUpProviderFixtureWith(t *testing.T, options string, transforms []string) (root, journal string, dependencies Dependencies) {
	t.Helper()

	root, _ = clitest.SetUpDeployFixture(t)
	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  transforms: `+mustJSON(t, transforms)+`,
  provider: { fake: `+options+` },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)

	journal = filepath.Join(t.TempDir(), "configure.journal")
	t.Setenv(clitest.FakeConfigureJournalEnvVar, journal)

	dependencies = newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	return root, journal, dependencies
}

func TestDeployConfiguresTheProviderOnceAtSessionSetup(t *testing.T) {
	root, journal, dependencies := setUpProviderFixtureWith(t,
		`{ location: "zone-b", certificates: { "app.acme.com": "fake-certificate/x" } }`,
		[]string{"./transforms/net.transform.ts"})

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("the provider was configured %d times, want exactly 1 for the session: %v", len(got), got)
	}
	want := "location=zone-b transforms=./transforms/net.transform.ts certificates=map[app.acme.com:fake-certificate/x]"
	if got[0] != want {
		t.Errorf("provider saw %q, want %q", got[0], want)
	}
}

func TestDeployRendersTheProviderRefusalAgainstTheConfigFile(t *testing.T) {
	root, _, dependencies := setUpProviderFixture(t, `{ locationn: "zone-b" }`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want options the provider refuses reported; stdout=%s", stdout.String())
	}
	rendered := stdout.String() + stderr.String()
	for _, want := range []string{
		`configures provider "fake" with options it does not accept`,
		`"provider.fake.locationn"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "invalid_argument:") {
		t.Errorf("rendered output = %q, want no raw connect code prefix", rendered)
	}
}

func TestADeployRecordsWhatItDeployed(t *testing.T) {
	t.Run("a successful deploy records the promotion, the tag and every app", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{{
			Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js",
			ArtifactPath: "output/api", App: "api",
		}})
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		writeServeDescriptor(t, root, "api", "bld_api_1")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true, tag: "v9"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployRecord(t, root)
		if got.Slug != "test-app" {
			t.Errorf("slug = %q, want the resolved config's", got.Slug)
		}
		if got.Environment.Tier != "production" {
			t.Errorf("environment.tier = %q, want %q", got.Environment.Tier, "production")
		}
		if got.Provider.Name != "fake" {
			t.Errorf("provider = %+v, want the config's provider", got.Provider)
		}
		if got.PromotionID != clitest.FakePromotionID {
			t.Errorf("promotionId = %q, want the provider's %q", got.PromotionID, clitest.FakePromotionID)
		}
		if got.Tag != "v9" {
			t.Errorf("tag = %q, want %q", got.Tag, "v9")
		}
		if len(got.Apps) == 0 || len(got.Apps[0].URLs) != 1 || got.Apps[0].URLs[0] != clitest.FakeAppURL {
			t.Errorf("apps = %+v, want the first app to have [%s]", got.Apps, clitest.FakeAppURL)
		}
		if len(got.Apps) != 1 || got.Apps[0].Name != "api" || got.Apps[0].BuildID != "bld_api_1" {
			t.Errorf("apps = %+v, want one api app with build id bld_api_1", got.Apps)
		}
		if got.DeployedAt.IsZero() {
			t.Error("deployedAt is zero, want the completion time")
		}
	})

	t.Run("a failed deploy leaves no stale result behind", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, _ := clitest.SetUpDeployFixture(t)
		if err := deployrecord.Write(root, deployrecord.Record{PromotionID: "prm_previous_run"}); err != nil {
			t.Fatalf("seed stale result: %v", err)
		}
		t.Setenv(clitest.FakeProviderModeEnvVar, "fail")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the simulated failure; stdout=%s", stdout.String())
		}

		if _, statErr := os.Stat(deployrecord.Path(root)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want no result file after a failed deploy", deployrecord.Path(root), statErr)
		}
	})

	t.Run("a successful preview up records the named preview", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{{
			Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js",
			ArtifactPath: "output/api", App: "api",
		}})
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		writeServeDescriptor(t, root, "api", "bld_api_1")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "e2e-42"}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		got := readDeployRecord(t, root)
		if got.Environment.Tier != "preview" || got.Environment.Identity != "e2e-42" {
			t.Errorf("environment = %+v, want the named preview", got.Environment)
		}
		if got.PromotionID != clitest.FakePromotionID {
			t.Errorf("promotionId = %q, want the provider's %q", got.PromotionID, clitest.FakePromotionID)
		}
		if len(got.Apps) == 0 || len(got.Apps[0].URLs) != 1 || got.Apps[0].URLs[0] != clitest.FakeAppURL {
			t.Errorf("apps = %+v, want the first app to have [%s]", got.Apps, clitest.FakeAppURL)
		}
	})
}

func readDeployRecord(t *testing.T, root string) deployrecord.Record {
	t.Helper()
	raw, err := os.ReadFile(deployrecord.Path(root))
	if err != nil {
		t.Fatalf("read deploy result: %v", err)
	}
	var got deployrecord.Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("deploy result is not valid JSON: %v", err)
	}
	return got
}

func writeServeDescriptor(t *testing.T, root, app, buildID string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, statedir.Name, "output", "apps", app, edge.ServeDescriptorFile),
		`{"framework":"node","buildId":"`+buildID+`"}`)
}

func TestDeployLandsAUsageEdgeForEveryResourceAnAppReaches(t *testing.T) {
	t.Run("an app that uses a shared resource lands a usage edge naming the files it reaches through", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
		})
		root, sockPath := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "USAGE app=api resource=db--main files=apps/api/src/server.ts") {
			t.Errorf("stdout = %q, want the usage edge to have reached the manifest", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a resource no app uses still provisions and has no edge", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, sockPath := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want the orphan resource to deploy", out)
		}
		if strings.Contains(out, "USAGE ") {
			t.Errorf("stdout = %q, want no usage edge for an orphan resource", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("a runtime-computed import in an app fails the deploy closed", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)
		clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "late.ts"), `
const spec = "../../../shared/" + ["d", "b"].join("") + ".js";

export async function late() {
  return await import(spec);
}
`)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "apps/api/src/late.ts") {
			t.Errorf("output = %q, want it to name the file containing the unresolvable import", combined)
		}
	})
}

func TestDeployScopesDeliveryToTheUsingApps(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
		{Route: "web", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/web", App: "web"},
	})
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeSharedResourceMonorepo(t, root)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{
		"DELIVER app=api resources=bucket--uploads,db--main",
		"DELIVER app=web resources=db--main",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "DELIVER app=web resources=bucket--uploads") {
		t.Errorf("stdout = %q: web never reaches the bucket, so it receives neither its values nor its live keys", out)
	}

	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestDeployAttributesAnUnconfiguredProjectToItsOnlyApp(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output", App: clitest.FixtureSlug},
	})
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeRootApp(t, root)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DELIVER app="+clitest.FixtureSlug+" resources=db--main") {
		t.Errorf("stdout = %q, want the only app of a project that configures none to still reach what it declares", out)
	}

	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestDeployRefusesWhatItCannotAttribute(t *testing.T) {
	t.Run("a project that builds two apps and names neither", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/web", App: "web"},
		})
		root, _ := clitest.SetUpDeployFixture(t)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{"api", "web", "ocel.config.ts"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %q", combined, want)
			}
		}
	})

	t.Run("a built app the config names nothing of", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/legacy", App: "legacy"},
		})
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the app no configured app covers to refuse the deploy; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		if !strings.Contains(combined, "legacy") {
			t.Errorf("output = %q, want it to name the app the config covers with nothing", combined)
		}
	})

	t.Run("a configured path that names no directory", func(t *testing.T) {
		dependencies := newTestDependencies()
		stubBuild(&dependencies, []build.Function{
			{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
		})
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteUsageMonorepo(t, root)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/ap1", framework: "node" }],
};
`)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDeploy err = nil, want a path naming nothing to refuse the deploy rather than ship an app no resource reaches; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String() + err.Error()
		for _, want := range []string{`"api"`, "apps/ap1"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output = %q, want it to name %q", combined, want)
			}
		}
	})
}

func writeSharedResourceMonorepo(t *testing.T, root string) {
	t.Helper()

	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [
    { name: "api", path: "apps/api", framework: "node" },
    { name: "web", path: "apps/web", framework: "node" },
  ],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "files.ts"), `
import { declareBucket } from "./declare.js";

export const uploads = declareBucket("uploads");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./files.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, uploads } from "../../../shared/index.js";

export function handler() {
  return db.name + uploads.name;
}
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), `
import { db } from "../../../shared/index.js";

export function handler() {
  return db.name;
}
`)
}

func TestDeploySendsTheEdgeTheProjectDeclared(t *testing.T) {
	cases := []struct {
		name        string
		declaration string
		want        string
	}{
		{"an omitted edge names none, leaving the provider to choose", "", "kind= "},
		{"a declared api-gateway edge names it", "  edge: \"api-gateway\",\n", "kind=api-gateway"},
		{"a declared relay edge names it", "  edge: \"relay\",\n", "kind=relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, journal := clitest.SetUpEdgeFixture(t, tc.declaration)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, clitest.UsageMonorepoFunctions())

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			got := clitest.ReadJournal(t, journal)
			if len(got) != 1 {
				t.Fatalf("deploy reached the provider %d times, want exactly 1: %v", len(got), got)
			}
			if !strings.Contains(got[0], tc.want) {
				t.Errorf("provider saw %q, want %q", got[0], tc.want)
			}
		})
	}
}

func TestDeploySendsTheEdgeSettingsUnchanged(t *testing.T) {
	root, journal := clitest.SetUpEdgeFixture(t, "  edge: \"relay\",\n  dns: { zone: { zone: \"acme.com\" } },\n  allowDegraded: [\"streaming\", \"edge-cache\"],\n")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("deploy reached the provider %d times, want exactly 1: %v", len(got), got)
	}
	for _, want := range []string{"dns=zone/acme.com", "allowDegraded=streaming,edge-cache"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("provider saw %q, want it to include %q", got[0], want)
		}
	}
}

func TestDeployRendersAnEdgeTheOriginRefuses(t *testing.T) {
	const refusal = `this provider cannot front deployments with the "alb" edge; it supports api-gateway, relay, direct`

	root, _ := clitest.SetUpEdgeFixture(t, "")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, clitest.UsageMonorepoFunctions())
	t.Setenv(clitest.FakeEdgeRefusalEnvVar, refusal)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the refused edge to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	if !strings.Contains(rendered, refusal) {
		t.Errorf("rendered output = %q, want it to include %q", rendered, refusal)
	}
	if strings.Contains(rendered, "connection lost") {
		t.Errorf("rendered output = %q, want a refusal not to read as a lost connection", rendered)
	}
}

func nothingToDeployHeadline(t *testing.T, config string) string {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), config)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "main.ts"), "export {};\n")
	writeAppSource(t, root, "web", "api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}
	evs := envelopes(t, stdout.String())
	return evs[len(evs)-1].GetSummary().GetHeadline()
}

func TestAProjectWithoutAppsOrResourcesHasNothingToDeploy(t *testing.T) {
	headline := nothingToDeployHeadline(t, `
export default {
  slug: "test-app",
  provider: { fake: { location: "zone-b" } },
};
`)
	if want := "Nothing to deploy: test-app declares no apps or resources"; headline != want {
		t.Errorf("headline = %q, want %q", headline, want)
	}
}

func TestAppsThatBuildNoFunctionOrImageAreNamedWhenNothingIsLeftToDeploy(t *testing.T) {
	headline := nothingToDeployHeadline(t, `
export default {
  slug: "test-app",
  provider: { fake: { location: "zone-b" } },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
};
`)
	if want := "Nothing to deploy: 2 apps (web and api) built no function or image, and test-app declares no resources"; headline != want {
		t.Errorf("headline = %q, want %q", headline, want)
	}
}

var propagationCases = []struct {
	name  string
	spec  string
	want  string
	other []string
}{
	{
		name:  "instant",
		spec:  "0",
		other: []string{"propagates"},
	},
	{
		name:  "published",
		spec:  "5000:published",
		want:  "propagates within ~5 s",
		other: []string{"typical, not guaranteed"},
	},
	{
		name:  "unpublished",
		spec:  "5000",
		want:  "propagates in ~5 s (typical, not guaranteed)",
		other: []string{"propagates within"},
	},
}

func TestPropagationOnTheProductionDeployPromotionLine(t *testing.T) {
	for _, tc := range propagationCases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockPath := clitest.SetUpDeployFixture(t)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			t.Setenv(clitest.FakePropagationEnvVar, tc.spec)

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertPropagationNote(t, stdout.String(), tc.want, tc.other)
			clitest.WaitForNoStaleSocket(t, sockPath)
		})
	}
}

func TestPropagationOnThePreviewDeployPromotionLine(t *testing.T) {
	for _, tc := range propagationCases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockPath := clitest.SetUpDeployFixture(t)
			dependencies := newTestDependencies()
			stubBuild(&dependencies, nil)
			stubGit(&dependencies, "feature/login", "")
			t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
			t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
			t.Setenv(clitest.FakePropagationEnvVar, tc.spec)

			var stdout, stderr bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			if err := runPreviewUp(context.Background(), dependencies, root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
				t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
			}

			assertPropagationNote(t, stdout.String(), tc.want, tc.other)
			clitest.WaitForNoStaleSocket(t, sockPath)
		})
	}
}

func assertPropagationNote(t *testing.T, out, want string, absent []string) {
	t.Helper()
	if want != "" && !strings.Contains(out, want) {
		t.Errorf("output = %q, want it to include %q", out, want)
	}
	for _, unwanted := range absent {
		if strings.Contains(out, unwanted) {
			t.Errorf("output = %q, want no %q", out, unwanted)
		}
	}
}
