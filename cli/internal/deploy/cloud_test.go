package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
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

func gitCheckout(t *testing.T, branch string, detached bool) string {
	t.Helper()
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "")
	steps := [][]string{
		{"init", "--quiet", "--initial-branch", branch},
		{"add", "."},
		{"-c", "user.name=ocel", "-c", "user.email=ocel@example.com", "commit", "--quiet", "--message", "root"},
	}
	if detached {
		steps = append(steps, []string{"checkout", "--quiet", "--detach"})
	}
	for _, args := range steps {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func builderEnv(t *testing.T, started *cloudv1.StartJobRequest) func(string) string {
	t.Helper()
	encoded, err := protojson.Marshal(&cloudv1.JobContext{
		JobId: clitest.FakeCloudJobID, Slug: started.GetSlug(), PreviewLabel: started.GetPreview().GetLabel(), Git: started.GetGit(),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{commands.CloudJobContextEnvVar: path, executables.OverrideEnvVar: t.TempDir()}
	return func(name string) string { return env[name] }
}

func TestAPreviewOnOcelCloudIsTheSameEnvironmentInTheJobAsOnTheMachineThatStartedIt(t *testing.T) {
	for name, tc := range map[string]struct {
		branch    string
		detached  bool
		env       map[string]string
		target    Target
		identity  string
		label     string
		lifecycle environmentv1.Lifecycle
		pr        uint32
		gitBranch string
	}{
		"named": {
			branch: "main", target: Preview("staging", false),
			identity: "staging", lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, gitBranch: "main",
		},
		"named on a pull request": {
			branch: "feature/login", env: map[string]string{previewid.PRNumberEnvVar: "12"}, target: Preview("staging", true),
			identity: "staging", lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, pr: 12, gitBranch: "feature/login",
		},
		"the branch's": {
			branch: "feature/login", target: Preview("", false),
			identity: mustResolvePreview(t, "feature/login", "").Key, lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, gitBranch: "feature/login",
		},
		"a pull request's": {
			branch: "feature/login", env: map[string]string{previewid.PRNumberEnvVar: "12"}, target: Preview("", false),
			identity: mustResolvePreview(t, "feature/login", "12").Key, label: "pr-12", lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, pr: 12, gitBranch: "feature/login",
		},
		"a persistent one of the branch's": {
			branch: "feature/login", target: Preview("", true),
			identity: mustResolvePreview(t, "feature/login", "").Key, lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, gitBranch: "feature/login",
		},
		"a pull request's in GitHub Actions": {
			branch: "main", detached: true, env: map[string]string{"GITHUB_HEAD_REF": "feature/login", "GITHUB_REF": "refs/pull/34/merge"}, target: Preview("", false),
			identity: mustResolvePreview(t, "feature/login", "34").Key, label: "pr-34", lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, pr: 34, gitBranch: "feature/login",
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, unset := range []string{previewid.PRNumberEnvVar, "GITHUB_REF", "GITHUB_HEAD_REF"} {
				t.Setenv(unset, "")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			root := gitCheckout(t, tc.branch, tc.detached)
			fake := clitest.ServeOcelCloud(t)
			laptop := newTestDependencies()
			laptop.ReadGitBranch, laptop.DiscoverPRNumber = previewid.ReadGitBranch, previewid.DiscoverPRNumber
			fake.LogIn(&laptop.Invocation)
			clitest.AttachTerminalSink(laptop.Invocation, io.Discard)
			if err := To(t.Context(), laptop, root, tc.target, Options{}, io.Discard, io.Discard, strings.NewReader("")); err != nil {
				t.Fatal(err)
			}
			if len(fake.Started) != 1 {
				t.Fatalf("started %d jobs, want one", len(fake.Started))
			}
			started := fake.Started[0]
			want := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Lifecycle: tc.lifecycle, Identity: tc.identity, Label: tc.label}
			onLaptop, err := tc.target.environment(laptop, root)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(onLaptop, want) {
				t.Fatalf("on the laptop the preview is %v, want %v", onLaptop, want)
			}
			sent := &cloudv1.PreviewTarget{Name: tc.identity, Label: tc.label, Persistent: tc.lifecycle == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT}
			if !proto.Equal(started.GetPreview(), sent) {
				t.Fatalf("the job was started for the preview %v, want %v", started.GetPreview(), sent)
			}
			if started.GetGit().GetBranch() != tc.gitBranch || started.GetGit().GetPullRequest() != tc.pr || started.GetGit().GetCommit() == "" {
				t.Fatalf("the job was started with git %v, want branch %s, pull request %d and the commit", started.GetGit(), tc.gitBranch, tc.pr)
			}

			builder := newTestDependencies()
			job, err := commands.ReadBuilderJob(builderEnv(t, started))
			if err != nil {
				t.Fatal(err)
			}
			builder.BuilderJob = job
			builder.ReadGitBranch, builder.DiscoverPRNumber = builder.BuilderJob.ReadGitBranch, builder.BuilderJob.DiscoverPRNumber
			inJob, err := Preview(started.GetPreview().GetName(), started.GetPreview().GetPersistent()).environment(builder, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(inJob, onLaptop) {
				t.Fatalf("in the job the preview is %v, want the laptop's %v", inJob, onLaptop)
			}
			if source := builder.BuilderJob.Source(); source.GetBranch() != tc.gitBranch || source.GetCommit() != started.GetGit().GetCommit() {
				t.Fatalf("in the job the source is %v, want the laptop's git %v", source, started.GetGit())
			}
		})
	}
}

func mustResolvePreview(t *testing.T, branch, pr string) previewid.Identity {
	t.Helper()
	id, err := previewid.Resolve(branch, pr)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
