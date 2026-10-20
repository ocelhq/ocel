package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestADeployOfAProjectNamingNoProviderUploadsItsSourceToOcelCloudBeforeAnythingRunsLocally(t *testing.T) {
	for name, target := range map[string]Target{"deploy": Production(""), "preview up": Preview("feature", false)} {
		t.Run(name, func(t *testing.T) {
			fake := clitest.ServeOcelCloud(t)
			dependencies := newTestDependencies()
			fake.LogIn(&dependencies.Invocation)
			dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
				t.Fatal("built locally for an Ocel Cloud target")
				return build.Output{}, nil
			}
			root := t.TempDir()
			clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
			clitest.WriteFile(t, filepath.Join(root, "index.js"), "")

			var stdout bytes.Buffer
			clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
			err := To(t.Context(), dependencies, root, target, Options{}, &stdout, io.Discard, strings.NewReader(""))

			var coded *clierror.Error
			if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), target.command()) {
				t.Fatalf("err = %v, want %s stopped after the upload", err, target.command())
			}
			if got := fake.CommittedPaths(); len(got) != 2 {
				t.Fatalf("committed %q, want the project's source", got)
			}
		})
	}
}

func TestADeployToOcelCloudRefusesWhatOnlyALocalDeployDoes(t *testing.T) {
	for flag, opts := range map[string]Options{"--dry": {Dry: true}, "--prebuilt": {Prebuilt: true}} {
		t.Run(flag, func(t *testing.T) {
			fake := clitest.ServeOcelCloud(t)
			dependencies := newTestDependencies()
			fake.LogIn(&dependencies.Invocation)
			root := t.TempDir()
			clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)

			err := To(t.Context(), dependencies, root, Production(""), opts, io.Discard, io.Discard, strings.NewReader(""))
			var coded *clierror.Error
			if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), flag) {
				t.Fatalf("err = %v, want %s refused on Ocel Cloud", err, flag)
			}
			if fake.Committed != nil {
				t.Fatal("uploaded the source of a run it refused")
			}
		})
	}
}
