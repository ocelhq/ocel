package deploy

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func newBuildSpan(t *testing.T) (*run.Span, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewSink(terminal.Resolve(terminal.Conditions{Verbose: true}), &out))
	t.Cleanup(func() { _ = bus.Close() })
	_, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatalf("Begin() = %v", err)
	}
	return run.Phase(progressv1.Phase_PHASE_BUILD), &out
}

func recordBuildApp(deps *cmddeps.Deps) *bool {
	clitest.StubRecordedDeploymentIDs(deps)
	ran := false
	deps.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		ran = true
		return functionsOnDisk(deps, cfg)
	}
	return &ran
}

func functionsOnDisk(deps *cmddeps.Deps, cfg *project.Project) (build.Output, error) {
	functions, err := deps.ReadFunctions(cfg.Dir)
	if errors.Is(err, build.ErrNoBuildOutput) {
		return build.Output{}, nil
	}
	return build.Output{Functions: functions}, err
}

func declarationsWithClientValue(t *testing.T, cfg *project.Project, value string) *variables.Declarations {
	t.Helper()
	cell := variables.Cell{Key: "PUBLIC_SITE_URL"}
	declarations := variables.NewDeclarations(oneValue{cell: cell, value: value}, variablescope.Of(cfg, environmentv1.Tier_TIER_PRODUCTION, ""))
	if err := declarations.Prefetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := declarations.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{
		Definitions: []*resourcesv1.VariableDefinition{{
			Key:              cell.Key,
			Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
			ClientAccessible: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return declarations
}

type oneValue struct {
	cell  variables.Cell
	value string
}

func (v oneValue) List(context.Context) ([]variables.ValueMetadata, error) {
	return []variables.ValueMetadata{{Coordinate: variables.Coordinate{Cell: v.cell}}}, nil
}

func (v oneValue) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return map[variables.Coordinate]string{{Cell: v.cell}: v.value}, nil
}

func prebuiltConfig(root string) *project.Project {
	return &project.Project{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []project.App{{Name: "api", Path: ".", Compute: "serverless", Serverless: &project.Serverless{Framework: appbuild.FrameworkNode}}},
	}
}

func emptyDeclarations(cfg *project.Project) *variables.Declarations {
	return variables.NewDeclarations(emptyValues{}, variablescope.Of(cfg, environmentv1.Tier_TIER_PRODUCTION, ""))
}

type emptyValues struct{}

func (emptyValues) List(context.Context) ([]variables.ValueMetadata, error) { return nil, nil }

func (emptyValues) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return nil, nil
}

func recordedClientValue() clientenv.App {
	return clientenv.App{Name: "api", Variables: []variables.Variable{{
		Key:              "PUBLIC_SITE_URL",
		Class:            resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Value:            "https://example.com",
		ClientAccessible: true,
	}}}
}

func TestCollectAndBuildManifest(t *testing.T) {
	t.Run("--prebuilt skips the build and deploys the prebuilt tree's function", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		ran := recordBuildApp(&deps)

		s, out := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		manifest, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		if *ran {
			t.Error("the app build ran under --prebuilt, want it skipped")
		}

		functions := manifest.GetApps()[0].GetServerless().GetFunctions()
		if len(functions) != 1 {
			t.Fatalf("manifest has %d functions, want the prebuilt one: %+v", len(functions), functions)
		}
		if got, want := functions[0].GetLogicalName(), "fn--api--index"; got != want {
			t.Errorf("function logical name = %q, want %q", got, want)
		}
		if got, want := functions[0].GetArtifactPath(), "apps/api/functions/index.func"; got != want {
			t.Errorf("artifact path = %q, want the prebuilt tree's %q", got, want)
		}
		if !strings.Contains(out.String(), "prebuilt") {
			t.Errorf("build output = %q, want it to report that prebuilt output was used", out.String())
		}
	})

	t.Run("without --prebuilt the build runs", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		ran := recordBuildApp(&deps)

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		if _, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), phase: s, span: s}); err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		if !*ran {
			t.Error("the app build was skipped without --prebuilt, want it to run")
		}
	})

	t.Run("--prebuilt with no build output errors", func(t *testing.T) {
		deps := clitest.NewDeps()
		recordBuildApp(&deps)

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(t.TempDir())
		_, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble succeeded with no build output, want error")
		}
		if !strings.Contains(err.Error(), "ocel build") {
			t.Errorf("error = %q, want it to point at `ocel build`", err)
		}
	})

	t.Run("--prebuilt passes the id the output tree recorded for each app", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		recorded := "d1a2b3c4d5e6f708192a3b4c5d6e7f80"
		clitest.WriteFile(t, filepath.Join(root, constants.ProjectStateDirName, "output", "apps", "api", "deployment-id"), recorded+"\n")
		deps := clitest.NewDeps()

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		manifest, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
		apps := manifest.GetApps()
		if len(apps) != 1 || apps[0].GetDeploymentId() != recorded {
			ids := make([]string, 0, len(apps))
			for _, app := range apps {
				ids = append(ids, app.GetDeploymentId())
			}
			t.Errorf("manifest apps have deployments %q, want api alone having %q", ids, recorded)
		}
	})

	t.Run("--prebuilt refuses an app the output tree recorded no id for", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		_, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble succeeded for an app no build stamped, want error")
		}
		if !strings.Contains(err.Error(), "ocel build") || !strings.Contains(err.Error(), "api") {
			t.Errorf("error = %q, want it to name the app and point at `ocel build`", err)
		}
	})

	t.Run("the client accessor is generated before the build", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		generated := ""
		deps := clitest.NewDeps()
		clitest.StubRecordedDeploymentIDs(&deps)
		deps.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
			data, err := os.ReadFile(filepath.Join(root, constants.ProjectStateDirName, "env-client.ts"))
			if err != nil {
				return build.Output{}, err
			}
			generated = string(data)
			return functionsOnDisk(&deps, cfg)
		}

		s, _ := newBuildSpan(t)
		cfg := prebuiltConfig(root)
		if _, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarationsWithClientValue(t, cfg, "https://example.com"), phase: s, span: s}); err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}

		if !strings.Contains(generated, `PUBLIC_SITE_URL: inlined(schema, "PUBLIC_SITE_URL", process.env.PUBLIC_SITE_URL)`) {
			t.Errorf("accessor the build saw = %q, want it to read the key under its declared name", generated)
		}
		if _, err := os.Stat(filepath.Join(root, constants.ProjectStateDirName, "output", "client-digests.json")); err != nil {
			t.Errorf("the build recorded no client values: %v", err)
		}
	})

	t.Run("--prebuilt refuses a stale client value", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)
		cfg := prebuiltConfig(root)

		if err := clientenv.Record(root, []clientenv.App{recordedClientValue()}); err != nil {
			t.Fatal(err)
		}

		s, _ := newBuildSpan(t)
		declarations := declarationsWithClientValue(t, cfg, "https://rotated.example.com")
		_, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarations, prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble = nil for a build predating the client value, want a refusal")
		}
		if !strings.Contains(err.Error(), "PUBLIC_SITE_URL") {
			t.Errorf("error = %q, want it to name the changed key", err)
		}
		if !strings.Contains(err.Error(), "--prebuilt") {
			t.Errorf("error = %q, want it to name the flag being refused", err)
		}
	})

	t.Run("--prebuilt names an `ocel build` output for what it is", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)
		cfg := prebuiltConfig(root)
		if err := clientenv.Record(root, []clientenv.App{{Name: "api"}}); err != nil {
			t.Fatal(err)
		}

		s, _ := newBuildSpan(t)
		_, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarationsWithClientValue(t, cfg, "https://example.com"), prebuilt: true, phase: s, span: s})
		if err == nil {
			t.Fatal("collectBuildAndAssemble = nil for an `ocel build` output, want a refusal")
		}
		for _, want := range []string{"PUBLIC_SITE_URL", "never inlined", "`ocel build`, which resolves no values"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to state %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "changed since") {
			t.Errorf("error = %q, want it not to claim the value changed", err)
		}
	})

	t.Run("--prebuilt proceeds when the client value is unchanged", func(t *testing.T) {
		root := t.TempDir()
		clitest.WritePrebuiltFunction(t, root, "api", "index")
		deps := clitest.NewDeps()
		recordBuildApp(&deps)
		cfg := prebuiltConfig(root)

		if err := clientenv.Record(root, []clientenv.App{recordedClientValue()}); err != nil {
			t.Fatal(err)
		}

		s, _ := newBuildSpan(t)
		declarations := declarationsWithClientValue(t, cfg, "https://example.com")
		if _, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "serverless"), declarations: declarations, prebuilt: true, phase: s, span: s}); err != nil {
			t.Fatalf("collectBuildAndAssemble: %v", err)
		}
	})
}

func TestPrebuiltFlag(t *testing.T) {
	for _, cmd := range []struct {
		name string
		want string
	}{
		{"deploy", "deploy"},
		{"preview", "preview"},
		{"preview up", "preview up"},
	} {
		t.Run("`ocel "+cmd.name+"` accepts --prebuilt", func(t *testing.T) {
			parts := strings.Fields(cmd.want)
			commands := map[string]*cobra.Command{
				"deploy":  NewCommand(cmddeps.Deps{}),
				"preview": NewPreviewCommand(cmddeps.Deps{}),
			}
			target := commands[parts[0]]
			for _, part := range parts[1:] {
				next, _, err := target.Find([]string{part})
				if err != nil || next == target {
					t.Fatalf("command %q not found: %v", cmd.want, err)
				}
				target = next
			}
			if target.Flags().Lookup("prebuilt") == nil {
				t.Errorf("`ocel %s` does not accept --prebuilt", cmd.want)
			}
		})
	}
}

func TestPrebuiltDeploy(t *testing.T) {
	t.Run("no build output aborts before the provider is spawned", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		addAppToFixtureConfig(t, root)
		if err := os.RemoveAll(filepath.Join(root, constants.ProjectStateDirName, "output")); err != nil {
			t.Fatalf("drop the fixture's build output: %v", err)
		}
		deps := clitest.NewDeps()
		recordBuildApp(&deps)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stdout)
		err := runDeploy(context.Background(), deps, root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want the missing build output reported")
		}
		if !strings.Contains(stdout.String(), "ocel build") {
			t.Errorf("stdout = %q, want it to point at `ocel build`", stdout.String())
		}
		if strings.Contains(stdout.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", stdout.String())
		}
	})
}

func TestPrebuiltDeploysTheImageTheBuildRecordedRatherThanBuildingOne(t *testing.T) {
	root := t.TempDir()
	deps := clitest.NewDeps()
	ran := recordBuildApp(&deps)
	var asked map[string]string
	deps.ReadPrebuilt = func(_ context.Context, _ *project.Project, archs map[string]string) (build.Output, error) {
		asked = archs
		return build.Output{Images: map[string]string{"api": clitest.FixtureImage("api")}}, nil
	}

	s, _ := newBuildSpan(t)
	cfg := &project.Project{
		Dir:  root,
		Slug: "prebuilt",
		Apps: []project.App{{Name: "api", Path: ".", Compute: "container"}},
	}
	archs := map[string]string{"api": "arm64"}
	manifest, _, err := collectBuildAndAssemble(context.Background(), deps, assembly{cfg: onCompute(cfg, "container"), declarations: emptyDeclarations(cfg), prebuilt: true, phase: s, span: s, containerArchs: archs})
	if err != nil {
		t.Fatalf("collectBuildAndAssemble: %v", err)
	}
	if *ran {
		t.Error("--prebuilt built the apps again, want the image the build recorded deployed as it is")
	}
	if !maps.Equal(asked, archs) {
		t.Errorf("the prebuilt image was checked against %v, want the %v the provider runs", asked, archs)
	}
	apps := manifest.GetApps()
	if len(apps) != 1 || apps[0].GetContainer().GetImage() != clitest.FixtureImage("api") {
		t.Errorf("the manifest deploys the apps %v, want api on the prebuilt %q", apps, clitest.FixtureImage("api"))
	}
}
