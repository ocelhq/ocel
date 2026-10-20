package build

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
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

	err := runBuild(t.Context(), dependencies, root, options{noWait: true}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "--no-wait") {
		t.Fatalf("err = %v, want --no-wait refused", err)
	}
}
