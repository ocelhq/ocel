package build

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestBuildOfAProjectNamingNoProviderUploadsItsSourceToOcelCloudWhenLoggedIn(t *testing.T) {
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
	err := runBuild(t.Context(), dependencies, root)

	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), "ocel build") {
		t.Fatalf("err = %v, want ocel build stopped after the upload", err)
	}
	if fake.Committed == nil {
		t.Fatal("the source was never committed")
	}
}
