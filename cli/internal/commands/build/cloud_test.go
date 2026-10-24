package build

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/project"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestBuildOfAProjectNamingNoProviderRunsAsAnOcelCloudBuildJobWhenLoggedIn(t *testing.T) {
	fake := clitest.ServeOcelCloud(t)
	dependencies := newTestDependencies()
	fake.LogIn(&dependencies.Invocation)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		t.Fatal("built locally for an Ocel Cloud target")
		return build.Output{}, nil
	}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runBuild(t.Context(), dependencies, root, options{}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if len(fake.Started) != 1 || fake.Started[0].GetCommand() != cloudv1.JobCommand_JOB_COMMAND_BUILD || fake.Started[0].GetSourceId() != clitest.FakeCloudSourceID {
		t.Fatalf("started %v, want one build job of the committed source", fake.Started)
	}
}

func TestBuildRefusesNoWaitWhenTheProjectDoesNotRunOnOcelCloud(t *testing.T) {
	dependencies := newTestDependencies()
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	err := runBuild(t.Context(), dependencies, root, options{noWait: true}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "--no-wait") {
		t.Fatalf("err = %v, want --no-wait refused", err)
	}
}

func TestBuildInsideAnOcelCloudJobSpawnsTheProviderOcelFromTheProvidersDirWithNoLock(t *testing.T) {
	clitest.ServeFake(t, fake.NewForProject(fake.Options{}))
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	clitest.InstallProvider(t, commands.CloudProviderID, func(dest string) error { return os.Symlink(testBinary, dest) })
	dependencies := newTestDependencies()
	dependencies.BuilderJob = &commands.BuilderJob{ID: "job_1", Slug: "shop", ProvidersDir: os.Getenv(executables.OverrideEnvVar)}
	var built *project.Project
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		built = cfg
		return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}}, nil
	}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "export {};\n")

	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runBuild(t.Context(), dependencies, root, options{}, strings.NewReader("")); err != nil {
		t.Fatalf("runBuild: %v\n%s", err, out.String())
	}
	if built == nil || built.Apps[0].Compute == "" {
		t.Fatalf("built %+v, want the app's compute resolved by the provider %s", built, commands.CloudProviderID)
	}
	if _, err := os.Stat(filepath.Join(root, lockfile.Name)); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v, want no lock written inside a job", lockfile.Name, err)
	}
}

func TestBuildWithAnEnvIsRefusedOnOcelCloud(t *testing.T) {
	cloud := clitest.ServeOcelCloud(t)
	dependencies := newTestDependencies()
	cloud.LogIn(&dependencies.Invocation)
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	err := runBuild(t.Context(), dependencies, root, options{env: "production"}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "ocel build --env") {
		t.Fatalf("err = %v, want ocel build --env refused on Ocel Cloud", err)
	}
	if len(cloud.Started) != 0 {
		t.Errorf("started %v, want no job: Ocel Cloud's build job takes no tier", cloud.Started)
	}
}
