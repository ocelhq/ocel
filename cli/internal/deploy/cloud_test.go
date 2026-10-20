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
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func TestADeployOfAProjectNamingNoProviderStartsAnOcelCloudJobBeforeAnythingRunsLocally(t *testing.T) {
	for name, tc := range map[string]struct {
		target Target
		want   func(t *testing.T, started *cloudv1.StartJobRequest)
	}{
		"deploy": {target: Production("v1.2.0"), want: func(t *testing.T, started *cloudv1.StartJobRequest) {
			if started.GetCommand() != cloudv1.JobCommand_JOB_COMMAND_DEPLOY || started.GetTag() != "v1.2.0" || started.GetPreview() != nil {
				t.Fatalf("started %v, want a deploy tagged v1.2.0", started)
			}
		}},
		"preview up": {target: Preview("feature", true), want: func(t *testing.T, started *cloudv1.StartJobRequest) {
			if started.GetCommand() != cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP || started.GetTag() != "" ||
				started.GetPreview().GetName() != "feature" || !started.GetPreview().GetPersistent() {
				t.Fatalf("started %v, want the persistent preview feature", started)
			}
		}},
	} {
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
			if err := To(t.Context(), dependencies, root, tc.target, Options{}, &stdout, io.Discard, strings.NewReader("")); err != nil {
				t.Fatal(err)
			}
			if got := fake.CommittedPaths(); len(got) != 2 {
				t.Fatalf("committed %q, want the project's source", got)
			}
			if len(fake.Started) != 1 {
				t.Fatalf("started %d jobs, want one", len(fake.Started))
			}
			if fake.Started[0].GetSlug() != "shop" || fake.Started[0].GetSourceId() != clitest.FakeCloudSourceID {
				t.Fatalf("started %v, want shop's committed source", fake.Started[0])
			}
			tc.want(t, fake.Started[0])
		})
	}
}

func TestADeployRefusesNoWaitWhenTheProjectDoesNotRunOnOcelCloud(t *testing.T) {
	dependencies := newTestDependencies()
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "provider": {"fake": {}}}`)

	err := To(t.Context(), dependencies, root, Production(""), Options{NoWait: true}, io.Discard, io.Discard, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "--no-wait") {
		t.Fatalf("err = %v, want --no-wait refused", err)
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
