package commands_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/ocelhq/ocel/cli/internal/confirmation"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
)

func runOnARunner(t *testing.T, invocation commands.Invocation, root, command string) (string, error) {
	t.Helper()
	return runRemoteJob(t, t.Context(), invocation, root, commands.RemoteRun{Command: command, Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY})
}

func runRemoteJob(t *testing.T, ctx context.Context, invocation commands.Invocation, root string, job commands.RemoteRun) (string, error) {
	t.Helper()
	if len(job.Tiers) == 0 {
		job.Tiers = []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}
	}
	target, err := invocation.ResolveTarget(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !target.IsCloud() {
		t.Fatal("the target is not Ocel Cloud")
	}
	var out bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	err = invocation.RunRemote(ctx, target, strings.NewReader(""), job)
	if closeErr := invocation.Events.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return out.String(), err
}

func deploymentPage(fake *clitest.FakeConsole) string {
	return fake.URL + "/projects/shop/deployments/" + clitest.FakeDeploymentID + "?org=org_1"
}

func writeShop(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shop")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "console.log('hi')")
	return root
}

func TestTheFirstRemoteRunLinksAConsoleProjectUploadsTheSourceAndRunsAJob(t *testing.T) {
	t.Setenv(previewid.PRNumberEnvVar, "")
	t.Setenv("GITHUB_REF", "")
	invocation, fake := loggedIn(t)
	root := writeShop(t)
	clitest.WriteFile(t, filepath.Join(root, ".env"), "SECRET=1")
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED, DeploymentId: "dep_1", Result: &runnerv1.JobResult{
		Urls: []string{"https://shop.ocel.app"},
	}}}

	out, err := runOnARunner(t, invocation, root, "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	_, afterOrganizations, _ := strings.Cut(out, "Loaded your organizations")
	_, afterLoading, _ := strings.Cut(afterOrganizations, "\n")
	first, _, _ := strings.Cut(strings.TrimSpace(afterLoading), "\n")
	if !strings.Contains(first, "shop") {
		t.Fatalf("first line after the organizations = %q, want the identity of the run", first)
	}
	if !strings.Contains(out, "Ocel Cloud") || !strings.Contains(out, "Acme Inc") || !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("output = %q, want the target named: Ocel Cloud, the organization and the console", out)
	}
	jobURL := deploymentPage(fake)
	if !strings.Contains(out, jobURL) {
		t.Fatalf("output = %q, want the console's job page %s", out, jobURL)
	}
	if !strings.Contains(out, "https://shop.ocel.app") || !strings.Contains(out, "dep_1") {
		t.Fatalf("output = %q, want the job's result", out)
	}
	if len(fake.Created) != 1 || fake.Created[0].GetSlug() != "shop" {
		t.Fatalf("created %v, want the console project shop", fake.Created)
	}
	link, err := console.ReadLink(root, fake.URL)
	if err != nil || link == nil || link.OrganizationID != "org_1" || link.ProjectName != "shop" {
		t.Fatalf("link = %+v, err = %v, want this directory linked to shop", link, err)
	}
	if got := fake.Runner.CommittedPaths(); !slices.Equal(got, []string{"index.js", "ocel.json"}) {
		t.Fatalf("committed %q, want the source without .env or .ocel", got)
	}
	want := &runnerv1.JobRequest{Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY, Source: &runnerv1.JobRequest_SourceId{SourceId: clitest.FakeCloudSourceID}}
	if len(fake.Dispatched) != 1 || !proto.Equal(fake.Dispatched[0].GetJob(), want) {
		t.Fatalf("dispatched %v, want %s", clitest.DescribeDispatched(fake.Dispatched), clitest.DescribeJob(want))
	}
	dispatched := fake.Dispatched[0]
	if dispatched.GetRunnerId() != console.CloudEndpointID || dispatched.GetProvider() != "" || dispatched.GetSlug() != "shop" ||
		dispatched.GetTier() != environmentv1.Tier_TIER_PRODUCTION || len(dispatched.GetRequestId()) != 32 || dispatched.GetProjectId() != fake.Projects[0].GetId() {
		t.Fatalf("dispatched %v, want shop's production on Ocel Cloud's runner, naming no provider, under the run's trace id", clitest.DescribeDispatch(dispatched))
	}
	for _, bearer := range fake.Runner.Bearers {
		if !strings.HasPrefix(bearer, "Bearer "+clitest.FakeCloudToken) {
			t.Fatalf("Ocel Cloud saw %q, want the token the console issued", bearer)
		}
	}
	if !slices.Equal(fake.TokenEndpoints, []string{console.CloudEndpointID}) {
		t.Fatalf("tokens issued for %v, want one for Ocel Cloud's runner", fake.TokenEndpoints)
	}

	again, err := runOnARunner(t, newInvocationLoggedInTo(fake), root, "ocel deploy")
	if err != nil {
		t.Fatalf("second run err = %v", err)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created %v, want the linked project reused", fake.Created)
	}
	if !strings.Contains(again, "already holds every file") {
		t.Fatalf("second run = %q, want nothing uploaded again", again)
	}
}

func TestTheFirstRemoteRunNamesItsReadsOfTheConsoleAndTheCommitOfItsSource(t *testing.T) {
	invocation, fake := loggedIn(t)
	root := writeShop(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runOnARunner(t, invocation, root, "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	for _, named := range []struct{ phase, ended string }{
		{"check", "Loaded your organizations"},
		{"check", "Loaded the projects in Acme Inc"},
		{"check", "Read the runner of production"},
		{"build", "Committed the source to runner ocel-cloud"},
	} {
		line := ""
		for candidate := range strings.Lines(out) {
			if strings.Contains(candidate, named.ended) {
				line = candidate
				break
			}
		}
		switch {
		case line == "":
			t.Errorf("output = %q, want %q reported", out, named.ended)
		case !strings.Contains(line, "["+named.phase+"]"):
			t.Errorf("%q was reported as %q, want it under [%s]", named.ended, line, named.phase)
		}
	}
}

func TestARemoteJobsLinkIsALineOfItsOwnThatNoFoldedGroupHides(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{GitHubActions: true, ColorAsked: terminal.ColorNever})
	}
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	link := "INFO  [deploy] Job " + clitest.FakeJobID + " · follow it at " + deploymentPage(fake) + "\n"
	at := strings.Index(out, link)
	if at < 0 || strings.Count(out[:at], "::group::") != strings.Count(out[:at], "::endgroup::") {
		t.Fatalf("output = %q, want %q outside every group", out, link)
	}
}

func TestARemoteJobCarriesTheGitContextReadOnTheLaptop(t *testing.T) {
	clitest.UnsetGitEnv()
	invocation, fake := loggedIn(t)
	root := writeShop(t)
	for _, args := range [][]string{{"init", "-q", "-b", "feat/cart"}, {"add", "."}, {"-c", "user.email=dev@acme.test", "-c", "user.name=dev", "commit", "-q", "-m", "shop"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "console.log('changed')")
	t.Setenv(previewid.PRNumberEnvVar, "12")

	if _, err := runOnARunner(t, invocation, root, "ocel deploy"); err != nil {
		t.Fatal(err)
	}
	git := fake.Dispatched[0].GetJob().GetGit()
	if len(git.GetCommit()) != 40 || git.GetBranch() != "feat/cart" || !git.GetDirty() || git.GetPullRequest() != 12 {
		t.Fatalf("git = %v, want the commit, feat/cart, dirty and pull request 12", git)
	}
}

func TestARemoteJobNamesWhereTheProjectSitsInTheUploadedSource(t *testing.T) {
	invocation, fake := loggedIn(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, "apps", "web")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "web"}`)
	clitest.WriteFile(t, filepath.Join(repo, "package.json"), `{}`)

	if _, err := runOnARunner(t, invocation, root, "ocel deploy"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Dispatched[0].GetJob().GetProjectPath(); got != "apps/web" {
		t.Fatalf("project path = %q, want apps/web", got)
	}
	if got := fake.Runner.CommittedPaths(); !slices.Equal(got, []string{"apps/web/ocel.json", "package.json"}) {
		t.Fatalf("committed %q, want the whole repository", got)
	}
}

func TestARemoteJobRunsWithTheConfigFileTheCommandSelected(t *testing.T) {
	preview := &runnerv1.PreviewTarget{Name: "feat-cart"}
	for _, run := range []commands.RemoteRun{
		{Command: "ocel deploy", Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY},
		{Command: "ocel preview up", Kind: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PREVIEW}, Preview: preview},
		{Command: "ocel preview rm", Kind: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PREVIEW}, Preview: preview},
		{Command: "ocel projects destroy", Kind: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY},
	} {
		t.Run(run.Command, func(t *testing.T) {
			invocation, fake := loggedIn(t)
			root := writeShop(t)
			clitest.WriteFile(t, filepath.Join(root, "ocel.staging.json"), `{"slug": "shop"}`)
			t.Setenv(commands.ConfigEnvVar, filepath.Join(root, "ocel.staging.json"))
			fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

			if _, err := runRemoteJob(t, t.Context(), invocation, root, run); err != nil {
				t.Fatal(err)
			}
			if len(fake.Dispatched) != 1 || fake.Dispatched[0].GetJob().GetConfigFile() != "ocel.staging.json" {
				t.Fatalf("dispatched %v, want the job run with ocel.staging.json", clitest.DescribeDispatched(fake.Dispatched))
			}
		})
	}
}

func TestARemoteJobThatFailsFailsTheRunWithItsErrorAndLink(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_FAILED, Error: &runnerv1.JobError{Code: "build.failed", Message: "app web failed to build"}}}

	out, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeJobFailed {
		t.Fatalf("err = %v, want %s", err, clierror.CodeJobFailed)
	}
	for _, want := range []string{"build.failed", "app web failed to build", deploymentPage(fake)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q named", err, want)
		}
	}
	if !strings.Contains(out, "app web failed to build") {
		t.Fatalf("output = %q, want the failure shown", out)
	}
}

func TestWhatARunnerSaysOfAJobReachesTheTerminalWithItsControlCharactersEscaped(t *testing.T) {
	const clear = "\x1b[2J"
	t.Run("a job that succeeds", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Runner.JobReads = []*runnerv1.Job{
			{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_RUNNING, Step: "Building" + clear},
			{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED, DeploymentId: "d1" + clear, Result: &runnerv1.JobResult{
				Urls:   []string{"https://shop.ocel.app" + clear},
				Builds: []*runnerv1.AppBuild{{App: "web" + clear, BuildId: "b1" + clear}},
			}},
		}
		out, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "\x1b") || !strings.Contains(out, `Building\u001b[2J`) || !strings.Contains(out, `https://shop.ocel.app\u001b[2J`) {
			t.Fatalf("output = %q, want each escape Ocel Cloud sent shown escaped", out)
		}
	})

	t.Run("a job that fails", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_FAILED, Error: &runnerv1.JobError{Code: "build.failed" + clear, Message: "app web failed" + clear}}}
		_, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
		if err == nil || strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), `app web failed\u001b[2J`) {
			t.Fatalf("err = %q, want the failure with its escapes shown escaped", err)
		}
	})
}

func TestARemoteJobThatFailsWithARunnersCodeLinksThatCodesPage(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_FAILED, Error: &runnerv1.JobError{Code: "job.timeout", Message: "the job ran past its token's expiry"}}}

	_, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeJobFailed || got.GetDocsUrl() != clierror.FormatErrorPage("job.timeout") {
		t.Fatalf("run error = %v, want %s linking job.timeout's page", got, clierror.CodeJobFailed)
	}
}

func TestARemoteJobThatFailsWithACodeOfOcelCloudsOwnLinksThatCodesPage(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_FAILED, Error: &runnerv1.JobError{Code: "cloud.preview_limit", Message: "the plan allows 3 previews"}}}

	_, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeJobFailed || got.GetDocsUrl() != clierror.FormatErrorPage("cloud.preview_limit") {
		t.Fatalf("run error = %v, want %s linking cloud.preview_limit's page", got, clierror.CodeJobFailed)
	}
}

func TestARemoteDeployThatDeployedNothingEndsWithTheJobsNoticeNotDeployed(t *testing.T) {
	invocation, fake := loggedIn(t)
	notice := `Nothing to deploy: shop declares no apps or resources, and no app is detected at the project root: name the app to deploy under "apps"`
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED, Step: notice}}

	out, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Deployed shop") {
		t.Fatalf("output = %q, want no claim of a deployment from a job that deployed nothing", out)
	}
	if !strings.Contains(out, notice+" · job "+deploymentPage(fake)) {
		t.Fatalf("output = %q, want the job's notice and its link", out)
	}
}

func TestARemoteJobThatIsCanceledFailsTheRun(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_CANCELED}}

	_, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeJobCanceled {
		t.Fatalf("err = %v, want %s", err, clierror.CodeJobCanceled)
	}
}

func TestARemoteRunWithNoWaitReturnsOnceTheJobStarts(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.OnJobRead = func(*runnerv1.Job) { t.Error("read the job of a run that does not wait") }

	out, err := runRemoteJob(t, t.Context(), invocation, writeShop(t), commands.RemoteRun{Command: "ocel build", Kind: runnerv1.JobCommand_JOB_COMMAND_BUILD, NoWait: true})
	if err != nil {
		t.Fatal(err)
	}
	if page := fake.URL + "/projects/shop?org=org_1"; !strings.Contains(out, clitest.FakeJobID) || !strings.Contains(out, page) {
		t.Fatalf("output = %q, want the job id and the project's page %s", out, page)
	}
	if len(fake.Dispatched) != 1 {
		t.Fatalf("started %d jobs, want one", len(fake.Dispatched))
	}
}

func TestInterruptingARemoteRunStopsWatchingAndLeavesTheJobRunning(t *testing.T) {
	invocation, fake := loggedIn(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_RUNNING, Phase: runnerv1.JobPhase_JOB_PHASE_BUILD, Step: "Building web"}}
	fake.Runner.OnJobRead = func(*runnerv1.Job) { cancel() }

	out, err := runRemoteJob(t, ctx, invocation, writeShop(t), commands.RemoteRun{Command: "ocel deploy", Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatalf("err = %v, want stopping to watch to succeed", err)
	}
	if !strings.Contains(out, "keeps running") || !strings.Contains(out, deploymentPage(fake)) {
		t.Fatalf("output = %q, want the job named still running, with its link", out)
	}
	if len(fake.Runner.Canceled) != 0 {
		t.Fatalf("canceled %v, want the job left running", fake.Runner.Canceled)
	}
}

func needingInput(id string, need *runnerv1.JobInput) *runnerv1.Job {
	need.Id = id
	return &runnerv1.Job{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_INPUT_REQUIRED, Input: need}
}

func needingValues(id string, keys ...string) *runnerv1.Job {
	needed := &runnerv1.VariablesNeeded{Tier: environmentv1.Tier_TIER_PRODUCTION}
	for _, key := range keys {
		needed.Variables = append(needed.Variables, &runnerv1.NeededVariable{Key: key})
	}
	return needingInput(id, &runnerv1.JobInput{Need: &runnerv1.JobInput_Variables{Variables: needed}})
}

func TestARemoteJobThatNeedsInputOpensItsPageOncePerInputAndKeepsWaiting(t *testing.T) {
	invocation, fake := loggedIn(t)
	values := needingValues("in_1", "DATABASE_URL", "STRIPE_KEY")
	running := &runnerv1.Job{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_RUNNING}
	more := needingValues("in_2", "SENTRY_DSN")
	fake.Runner.JobReads = []*runnerv1.Job{values, values, running, more, {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}
	var opened []string
	invocation.StdinIsTerminal = func(io.Reader) bool { return true }
	invocation.OpenBrowser = func(_ context.Context, url string) error { opened = append(opened, url); return nil }

	out, err := runRemoteJob(t, t.Context(), invocation, writeShop(t), commands.RemoteRun{Command: "ocel deploy", Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatal(err)
	}
	jobURL := deploymentPage(fake)
	if !slices.Equal(opened, []string{jobURL, jobURL}) {
		t.Fatalf("opened %q, want the job page once for each input", opened)
	}
	if !strings.Contains(out, "2 variables are not ready") {
		t.Errorf("output = %q, want the values needed said", out)
	}
	if resumed := strings.Count(out, "Resumed — answered on the console"); resumed != 2 {
		t.Errorf("output = %q, want the run resumed each time the job stopped waiting, 2 times, not %d", out, resumed)
	}
}

func TestARemoteJobAskingAQuestionNobodyHereCanAnswerIsCanceledAndFailsNamingYes(t *testing.T) {
	invocation, fake := loggedIn(t)
	confirm := needingInput("in_1", &runnerv1.JobInput{Need: &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{Question: "Tear down the persistent preview p?"}}})
	fake.Runner.JobReads = []*runnerv1.Job{confirm}

	_, err := runRemoteJob(t, t.Context(), invocation, writeShop(t), commands.RemoteRun{Command: "ocel preview rm", Kind: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PREVIEW}, Preview: &runnerv1.PreviewTarget{Name: "p"}})
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeConfirmationRequired || got.GetHint() != "--yes" {
		t.Fatalf("run error = %v, want %s hinting --yes", got, clierror.CodeConfirmationRequired)
	}
	if !slices.Equal(fake.Runner.Canceled, []string{clitest.FakeJobID}) {
		t.Fatalf("canceled %v, want the job canceled rather than left waiting", fake.Runner.Canceled)
	}
}

func TestARemoteJobThatNeedsInputWithNoBrowserSaysWhereAndKeepsWaiting(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.JobReads = []*runnerv1.Job{needingValues("in_1", "DATABASE_URL"), {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runRemoteJob(t, t.Context(), invocation, writeShop(t), commands.RemoteRun{Command: "ocel deploy", Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatal(err)
	}
	asked := strings.Index(out, "✗ DATABASE_URL")
	page := strings.Index(out, "Fill them in on the console at:\n\n    "+deploymentPage(fake)+"\n")
	waited := strings.Index(out, "Waited for the job")
	if asked < 0 || page < asked || waited < page {
		t.Fatalf("output = %q, want the values needed and where to enter them, before the wait ends", out)
	}
}

func TestARemoteJobWaitingForInputHoldsTheRunOnItsPageUntilTheInputIsAnswered(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	values := needingValues("in_1", "DATABASE_URL")
	fake.Runner.JobReads = []*runnerv1.Job{values, {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runRemoteJob(t, t.Context(), invocation, writeShop(t), commands.RemoteRun{Command: "ocel deploy", Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatal(err)
	}
	var waiting *streamv1.WaitingEvent
	var resumed *streamv1.ResumedEvent
	for _, ev := range clitest.RunEvents(t, out) {
		if ev.GetWaiting() != nil {
			waiting = ev.GetWaiting()
		}
		if ev.GetResumed() != nil && waiting != nil {
			resumed = ev.GetResumed()
		}
	}
	want := &streamv1.WaitingEvent{
		Url:     deploymentPage(fake),
		Missing: &streamv1.MissingVariables{Cells: []*streamv1.MissingVariable{{Key: "DATABASE_URL"}}},
		Input:   values.GetInput(),
	}
	if !proto.Equal(waiting, want) {
		t.Fatalf("waiting = %v, want %v", waiting, want)
	}
	if resumed.GetReason() != "answered on the console" {
		t.Fatalf("resumed = %v, want the run resumed once the input is answered", resumed)
	}
}

func TestARunThatStopsWatchingAJobWaitingForInputResumesTheWait(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.Runner.JobReads = []*runnerv1.Job{needingValues("in_1", "DATABASE_URL")}
	reads := 0
	fake.Runner.OnJobRead = func(*runnerv1.Job) {
		if reads++; reads == 2 {
			cancel()
		}
	}

	out, err := runRemoteJob(t, ctx, invocation, writeShop(t), commands.RemoteRun{Command: "ocel deploy", Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatalf("err = %v, want stopping to watch to succeed", err)
	}
	held := false
	for _, ev := range clitest.RunEvents(t, out) {
		switch {
		case ev.GetWaiting() != nil:
			held = true
		case ev.GetResumed() != nil:
			held = false
		}
	}
	if held {
		t.Fatalf("output = %q, want every wait the run held resumed before it ended", out)
	}
}

func TestTheJSONSummaryOfARemoteRunNamesTheJob(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED, Result: &runnerv1.JobResult{Urls: []string{"https://shop.ocel.app"}}}}

	out, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	events := clitest.RunEvents(t, out)
	summary := events[len(events)-1].GetSummary()
	want := &streamv1.RemoteJob{
		Id:     clitest.FakeJobID,
		Url:    deploymentPage(fake),
		State:  runnerv1.JobState_JOB_STATE_SUCCEEDED,
		Result: &runnerv1.JobResult{Urls: []string{"https://shop.ocel.app"}},
		Runner: "ocel-cloud",
	}
	if jobs := summary.GetRemoteJobs(); !summary.GetSuccess() || len(jobs) != 1 || !proto.Equal(jobs[0], want) {
		t.Fatalf("summary = %v, want a success naming %v", summary, want)
	}
}

func newInvocationLoggedInTo(fake *clitest.FakeConsole) commands.Invocation {
	invocation := clitest.NewInvocation()
	fake.LogIn(&invocation)
	return invocation
}

func TestARemoteRunSelectsTheConsoleProjectWithTheProjectsSlug(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Projects = []*consolev1.Project{{Id: "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79", OrganizationId: "org_1", Name: "Shop", Slug: "shop"}}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)

	if _, err := runOnARunner(t, invocation, root, "ocel build"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Created) != 0 {
		t.Fatalf("created %v, want the existing project selected", fake.Created)
	}
	if link, _ := console.ReadLink(root, fake.URL); link == nil || link.ProjectID != "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79" {
		t.Fatalf("link = %+v, want the existing project", link)
	}
}

func TestARemoteRunDeploysUnderTheActiveOrganizationOrAsksForOne(t *testing.T) {
	two := []console.Organization{{ID: "org_1", Name: "Acme Inc", Slug: "acme"}, {ID: "org_2", Name: "Globex", Slug: "globex"}}

	t.Run("the active organization", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations, fake.ActiveOrg = two, "org_2"
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		out, _ := runOnARunner(t, invocation, root, "ocel deploy")
		if !strings.Contains(out, "Globex") {
			t.Fatalf("output = %q, want Globex named", out)
		}
		if link, _ := console.ReadLink(root, fake.URL); link == nil || link.OrganizationID != "org_2" {
			t.Fatalf("link = %+v, want org_2", link)
		}
		if !slices.Equal(fake.ActivatedOrgs, []string{"org_2"}) {
			t.Fatalf("activated %v, want org_2", fake.ActivatedOrgs)
		}
	})

	t.Run("a linked organization other than the active one", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations, fake.ActiveOrg = two, "org_2"
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		if err := console.WriteLink(root, console.Link{APIURL: fake.URL, OrganizationID: "org_1", ProjectID: clitest.FixtureConsoleProjectID, ProjectName: "shop"}); err != nil {
			t.Fatal(err)
		}
		fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}
		out, err := runOnARunner(t, invocation, root, "ocel deploy")
		if err != nil {
			t.Fatal(err)
		}
		if len(fake.ActivatedOrgs) != 0 {
			t.Fatalf("activated %v, want the console's active organization left alone", fake.ActivatedOrgs)
		}
		if len(fake.TokenOrgs) == 0 || slices.ContainsFunc(fake.TokenOrgs, func(org string) bool { return org != "org_1" }) {
			t.Fatalf("tokens issued for %v, want each for org_1, the linked organization", fake.TokenOrgs)
		}
		if !strings.Contains(out, "/projects/shop/deployments/"+clitest.FakeDeploymentID+"?org=org_1") {
			t.Fatalf("output = %q, want the job page naming org_1", out)
		}
		if want := []string{"apps.read", "apps.write"}; len(fake.TokenScopes) == 0 || slices.ContainsFunc(fake.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
			t.Fatalf("tokens issued with scopes %v, want each with exactly %v", fake.TokenScopes, want)
		}
	})

	t.Run("none active", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations = two
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		_, err := runOnARunner(t, invocation, root, "ocel deploy")
		var coded *clierror.Error
		if !errors.As(err, &coded) || coded.Code != clierror.CodeInputRequired || !strings.Contains(err.Error(), "ocel link --org") {
			t.Fatalf("err = %v, want ocel link --org asked for", err)
		}
		if len(fake.Created) != 0 || fake.Runner.Committed != nil || len(fake.Dispatched) != 0 {
			t.Fatal("linked, uploaded or started a job without an organization")
		}
	})
}

type tracedSpan struct {
	Name         string `json:"name"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Attributes   []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	} `json:"attributes"`
}

func (s tracedSpan) attribute(key string) string {
	for _, a := range s.Attributes {
		if a.Key == key {
			return a.Value.StringValue
		}
	}
	return ""
}

func readTracedSpans(t *testing.T, root string) map[string]tracedSpan {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(runtrace.RunsDir(root), "*", runtrace.TraceFileName))
	if err != nil || len(matches) != 1 {
		t.Fatalf("trace files = %v (err %v), want exactly one", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []tracedSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	byID := map[string]tracedSpan{}
	for _, resource := range doc.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, s := range scope.Spans {
				byID[s.SpanID] = s
			}
		}
	}
	return byID
}

func TestEachRequestARemoteRunSendsNestsUnderTheOperationThatSentIt(t *testing.T) {
	invocation, fake := loggedIn(t)
	root := writeShop(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	if _, err := runOnARunner(t, invocation, root, "ocel deploy"); err != nil {
		t.Fatal(err)
	}
	spans := readTracedSpans(t, root)
	sentUnder := map[string]string{}
	for _, s := range spans {
		if s.attribute("http.request.method") == "" {
			continue
		}
		url := s.attribute("url.full")
		parent, ok := spans[s.ParentSpanID]
		switch {
		case !ok:
			t.Errorf("request %s has parent %q, which is not in the trace", url, s.ParentSpanID)
		case parent.ParentSpanID == "":
			t.Errorf("request %s nests under the run's root span, want the operation that sent it", url)
		}
		for _, path := range []string{"/api/auth/organization/list", runnerv1connect.RunnerServiceCommitSourceProcedure, consolev1connect.JobServiceDispatchProcedure} {
			if strings.Contains(url, path) {
				sentUnder[path] = parent.Name
			}
		}
	}
	for path, want := range map[string]string{
		"/api/auth/organization/list":                      "Loading your organizations",
		runnerv1connect.RunnerServiceCommitSourceProcedure: "Committing the source to runner ocel-cloud",
		consolev1connect.JobServiceDispatchProcedure:       "Dispatching the job to runner ocel-cloud",
	} {
		if got := sentUnder[path]; got != want {
			t.Errorf("the request to %s nests under %q, want %q", path, got, want)
		}
	}
}

func runRemotely(t *testing.T, invocation commands.Invocation, root string, job commands.RemoteRun) (string, error) {
	t.Helper()
	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	err = invocation.RunRemote(t.Context(), target, strings.NewReader(""), job)
	if closeErr := invocation.Events.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return out.String(), err
}

func writeShopOnFake(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shop")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "provider": {"fake": {}}}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "console.log('hi')")
	return root
}

func productionDeploy() commands.RemoteRun {
	return commands.RemoteRun{Command: "ocel deploy", Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}, Kind: runnerv1.JobCommand_JOB_COMMAND_DEPLOY}
}

func TestARemoteRunOfAProjectNamingAProviderDispatchesToTheRunnerBoundToItsTier(t *testing.T) {
	invocation, fake := loggedIn(t)
	runner := fake.BindTestRunner(environmentv1.Tier_TIER_PRODUCTION, true)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED, DeploymentId: clitest.FakeDeploymentID}}

	out, err := runRemotely(t, invocation, writeShopOnFake(t), productionDeploy())
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if len(fake.Resolved) != 1 || fake.Resolved[0].GetProvider() != "fake" || fake.Resolved[0].GetTier() != environmentv1.Tier_TIER_PRODUCTION {
		t.Fatalf("resolved %v, want production's runner asked for with the provider ocel.json names", fake.Resolved)
	}
	if len(fake.Dispatched) != 1 || fake.Dispatched[0].GetRunnerId() != runner || fake.Dispatched[0].GetProvider() != "fake" {
		t.Fatalf("dispatched %v, want the job on the bound runner, naming the provider", clitest.DescribeDispatched(fake.Dispatched))
	}
	if !slices.Equal(fake.TokenEndpoints, []string{runner}) {
		t.Fatalf("tokens issued for %v, want each for the bound runner", fake.TokenEndpoints)
	}
	if !strings.Contains(out, "Runs on runner build-box") || !strings.Contains(out, "Deployed shop on runner build-box") {
		t.Fatalf("output = %q, want the runner named", out)
	}
}

func TestARemoteRunThatCannotRunUploadsAndDispatchesNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		arrange func(fake *clitest.FakeConsole)
		root    func(t *testing.T) string
		want    string
	}{
		"a tier no runner is bound to": {arrange: func(*clitest.FakeConsole) {}, root: writeShopOnFake, want: "bind a runner"},
		"a runner bound without target credentials": {arrange: func(fake *clitest.FakeConsole) {
			fake.BindTestRunner(environmentv1.Tier_TIER_PRODUCTION, false)
		}, root: writeShopOnFake, want: "without target credentials"},
	} {
		t.Run(name, func(t *testing.T) {
			invocation, fake := loggedIn(t)
			tc.arrange(fake)
			_, err := runRemotely(t, invocation, tc.root(t), productionDeploy())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if fake.Runner.Committed != nil || len(fake.Dispatched) != 0 || len(fake.Runner.Bearers) != 0 {
				t.Fatalf("committed %v, dispatched %v and reached the runner %d times, want nothing uploaded or dispatched", fake.Runner.CommittedPaths(), clitest.DescribeDispatched(fake.Dispatched), len(fake.Runner.Bearers))
			}
		})
	}
}

func TestARemoteRunNeedsAConsoleLogin(t *testing.T) {
	invocation := clitest.NewInvocation()
	_, err := runRemotely(t, invocation, writeShopOnFake(t), productionDeploy())
	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeConsoleNotLoggedIn || !strings.Contains(err.Error(), "ocel login") {
		t.Fatalf("err = %v (%s), want %s naming ocel login", err, code, clierror.CodeConsoleNotLoggedIn)
	}
}

func TestARemoteRunWithYesAnswersTheJobsConfirmationsAndKeepsWaiting(t *testing.T) {
	invocation, fake := loggedIn(t)
	confirm := needingInput("in_1", &runnerv1.JobInput{Need: &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{Question: "Replace it?"}}})
	fake.Runner.JobReads = []*runnerv1.Job{confirm, {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}
	job := productionDeploy()
	job.Yes = true

	out, err := runRemotely(t, invocation, writeShop(t), job)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.Runner.Answered, []string{"in_1"}) || !strings.Contains(out, "Answered yes: Replace it?") {
		t.Fatalf("answered %v, output = %q, want the question answered yes", fake.Runner.Answered, out)
	}
	if strings.Contains(out, "Answer it on the console") {
		t.Fatalf("output = %q, want no wait on the console for a question --yes answered", out)
	}
}

func TestARemoteRunShowsTheJobsEventsOnceTheRunnerTakesTheJob(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.Unaware = 2
	fake.Runner.JobEvents = []*runnerv1.JobEvent{{Seq: 1, Message: "Cloning the source"}, {Seq: 2, Level: runnerv1.JobEventLevel_JOB_EVENT_LEVEL_WARN, Message: "the cache is cold"}}
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runRemotely(t, invocation, writeShop(t), productionDeploy())
	if err != nil {
		t.Fatal(err)
	}
	waiting, cloning, warned := strings.Index(out, "Waiting for runner ocel-cloud to take the job"), strings.Index(out, "Cloning the source"), strings.Index(out, "WARN")
	if waiting < 0 || cloning < waiting || warned < 0 || strings.Count(out, "Cloning the source") != 1 {
		t.Fatalf("output = %q, want the wait for the runner, then each event once, the warning as a warning", out)
	}
}

type answeringPrompt struct {
	answer bool
	asked  []string
}

func (p *answeringPrompt) Attended() bool { return true }

func (p *answeringPrompt) Phrase(context.Context, string, string) (bool, error) { return p.answer, nil }

func (p *answeringPrompt) Confirm(_ context.Context, question string) (bool, error) {
	p.asked = append(p.asked, question)
	return p.answer, nil
}

func TestARemoteRunAttendedShowsTheJobsPlanAndAnswersWhatThePersonAnswers(t *testing.T) {
	for _, answer := range []bool{true, false} {
		t.Run(fmt.Sprint(answer), func(t *testing.T) {
			invocation, fake := loggedIn(t)
			prompt := &answeringPrompt{answer: answer}
			var shown bytes.Buffer
			invocation.Questions = providerprocess.Questions{Respondent: confirmation.Terminal{Prompt: prompt, Out: &shown}}
			confirm := needingInput("in_1", &runnerv1.JobInput{Need: &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{
				Finding: "This will permanently destroy production project \"shop\":\n  - db", Question: "Apply this plan to shop?",
			}}})
			fake.Runner.JobReads = []*runnerv1.Job{confirm, {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

			out, err := runRemotely(t, invocation, writeShop(t), commands.RemoteRun{Command: "ocel projects destroy", Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}, Kind: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(prompt.asked, []string{"Apply this plan to shop?"}) || !strings.Contains(shown.String(), "permanently destroy production project") {
				t.Fatalf("asked %q after showing %q, want the job's plan shown and its question asked", prompt.asked, shown.String())
			}
			want := "Answered yes"
			if !answer {
				want = "Answered no"
			}
			if !slices.Equal(fake.Runner.Answered, []string{"in_1"}) || !strings.Contains(out, want) {
				t.Fatalf("answered %v, output = %q, want the person's answer sent", fake.Runner.Answered, out)
			}
		})
	}
}

type typedValues struct {
	typed   map[string]string
	secrets []string
	asked   []string
}

func (v *typedValues) Input(_ context.Context, title, _ string) (string, bool, error) {
	v.asked = append(v.asked, title)
	return v.typed[title], true, nil
}

func (v *typedValues) Secret(_ context.Context, title, _ string) (string, bool, error) {
	v.asked = append(v.asked, title)
	v.secrets = append(v.secrets, title)
	return v.typed[title], true, nil
}

func needingSecret() *runnerv1.Job {
	needed := &runnerv1.VariablesNeeded{Tier: environmentv1.Tier_TIER_PRODUCTION, Variables: []*runnerv1.NeededVariable{
		{Key: "LOG_LEVEL"}, {Key: "STRIPE_KEY", Folder: "/billing", Secret: true},
	}}
	return needingInput("in_1", &runnerv1.JobInput{Need: &runnerv1.JobInput_Variables{Variables: needed}})
}

func TestARemoteRunAttendedSuppliesTheValuesAJobNeedsWithAVariablesWriteToken(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Questions = providerprocess.Questions{Respondent: confirmation.Terminal{Prompt: &answeringPrompt{}, Out: io.Discard}}
	values := &typedValues{typed: map[string]string{"LOG_LEVEL": "debug", "/billing/STRIPE_KEY": "sk_test_placeholder"}}
	invocation.Values = values
	fake.Runner.JobReads = []*runnerv1.Job{needingSecret(), {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runRemotely(t, invocation, writeShop(t), productionDeploy())
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Runner.Supplied) != 1 {
		t.Fatalf("supplied %v, want the values sent once", clitest.DescribeSupplied(fake.Runner.Supplied))
	}
	sent := fake.Runner.Supplied[0].GetVariables().GetValues()
	if len(sent) != 2 || sent[0].GetValue() != "debug" || sent[1].GetKey() != "STRIPE_KEY" || sent[1].GetFolder() != "/billing" || sent[1].GetValue() != "sk_test_placeholder" {
		t.Fatalf("sent %q, want each value under its key and folder", clitest.DescribeValueKeys(sent))
	}
	if !slices.Equal(values.secrets, []string{"/billing/STRIPE_KEY"}) {
		t.Fatalf("asked %v hidden, want only the secret", values.secrets)
	}
	if !slices.ContainsFunc(fake.TokenScopes, func(got []string) bool { return slices.Equal(got, []string{"variables.write"}) }) {
		t.Fatalf("tokens issued with %v, want one holding only variables.write", fake.TokenScopes)
	}
	if strings.Contains(out, "sk_test_placeholder") || strings.Contains(out, "Fill them in on the console") {
		t.Fatalf("output = %q, want no secret shown and no wait on the console", out)
	}
}

func TestARemoteRunWhoseRoleCannotSupplyValuesWaitsOnTheConsole(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Questions = providerprocess.Questions{Respondent: confirmation.Terminal{Prompt: &answeringPrompt{}, Out: io.Discard}}
	invocation.Values = &typedValues{typed: map[string]string{"LOG_LEVEL": "debug", "/billing/STRIPE_KEY": "sk_test_placeholder"}}
	fake.RefusedClaims = []string{"variables.write"}
	fake.Runner.JobReads = []*runnerv1.Job{needingSecret(), {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runRemotely(t, invocation, writeShop(t), productionDeploy())
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Runner.Supplied) != 0 || !strings.Contains(out, "Fill them in on the console") || !strings.Contains(out, "variables.write") {
		t.Fatalf("supplied %v, output = %q, want the refusal said and the run waiting on the console", clitest.DescribeSupplied(fake.Runner.Supplied), out)
	}
}

func TestARemoteRunWhoseConsoleRefusesToReadTheQueuedJobEnds(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.Unaware = 100
	fake.GetRefusal = connect.NewError(connect.CodeUnauthenticated, errors.New("the session has expired"))

	_, err := runRemotely(t, invocation, writeShop(t), productionDeploy())
	if err == nil || !strings.Contains(err.Error(), "the session has expired") {
		t.Fatalf("err = %v, want the console's refusal to end the wait", err)
	}
}

func TestARemoteRunRetriesADispatchTheConsoleCouldNotTakeWithTheSameRequest(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.DispatchFails = []error{connect.NewError(connect.CodeUnavailable, errors.New("the console is restarting"))}

	if _, err := runRemotely(t, invocation, writeShop(t), productionDeploy()); err != nil {
		t.Fatal(err)
	}
	if len(fake.DispatchTries) != 2 || fake.DispatchTries[0] != fake.DispatchTries[1] || len(fake.Dispatched) != 1 {
		t.Fatalf("tried %v and dispatched %d, want one retry with the same request id", fake.DispatchTries, len(fake.Dispatched))
	}

	fake.DispatchFails = []error{connect.NewError(connect.CodePermissionDenied, errors.New("your role cannot"))}
	if _, err := runRemotely(t, newInvocationLoggedInTo(fake), writeShop(t), productionDeploy()); err == nil || len(fake.DispatchTries) != 3 {
		t.Fatalf("err = %v after %d tries, want a refusal not retried", err, len(fake.DispatchTries))
	}
}

func TestInterruptingARemoteRunOfSeveralTiersEndsItNamingTheTiersNotDispatched(t *testing.T) {
	invocation, fake := loggedIn(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_RUNNING}}
	fake.Runner.OnJobRead = func(*runnerv1.Job) { cancel() }
	target, err := invocation.ResolveTarget(ctx, writeShop(t))
	if err != nil {
		t.Fatal(err)
	}
	clitest.AttachTerminalSink(invocation, io.Discard)
	err = invocation.RunRemote(ctx, target, strings.NewReader(""), commands.RemoteRun{
		Command: "ocel projects destroy", Kind: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY, Yes: true,
		Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW},
	})
	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeInterrupted || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("err = %v (%s), want an interruption naming preview", err, code)
	}
	if len(fake.Dispatched) != 1 {
		t.Fatalf("dispatched %d jobs, want only production's", len(fake.Dispatched))
	}
}

func TestARemoteRunInsideAJobIsRefusedBeforeItReachesTheConsole(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.JobContext = &jobcontext.Context{Job: &jobcontext.Job{ID: "job_1", Slug: "shop"}}
	_, err := runRemotely(t, invocation, writeShopOnFake(t), productionDeploy())
	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeUsage || !strings.Contains(err.Error(), "drop --remote") {
		t.Fatalf("err = %v (%s), want --remote refused inside a job", err, code)
	}
	if len(fake.Resolved) != 0 {
		t.Fatalf("resolved %v, want the console never asked", fake.Resolved)
	}
}

func TestARemoteRunWhoseRoleCannotSupplyValuesAsksForNone(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Questions = providerprocess.Questions{Respondent: confirmation.Terminal{Prompt: &answeringPrompt{}, Out: io.Discard}}
	values := &typedValues{typed: map[string]string{}}
	invocation.Values = values
	fake.RefusedClaims = []string{"variables.write"}
	fake.Runner.JobReads = []*runnerv1.Job{needingSecret(), {Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_SUCCEEDED}}

	if _, err := runRemotely(t, invocation, writeShop(t), productionDeploy()); err != nil {
		t.Fatal(err)
	}
	if len(values.asked) != 0 {
		t.Fatalf("asked for %v, want no value asked for when the role cannot send it", values.asked)
	}
}

func TestTheJSONSummaryOfARemoteRunOfSeveralTiersNamesEveryJob(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	target, err := invocation.ResolveTarget(t.Context(), writeShop(t))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	err = invocation.RunRemote(t.Context(), target, strings.NewReader(""), commands.RemoteRun{
		Command: "ocel projects destroy", Kind: runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY, Yes: true,
		Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW},
	})
	if closeErr := invocation.Events.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	events := clitest.RunEvents(t, out.String())
	jobs := events[len(events)-1].GetSummary().GetRemoteJobs()
	if len(jobs) != 2 || jobs[0].GetId() == jobs[1].GetId() || len(fake.Dispatched) != 2 {
		t.Fatalf("summary names %v, want both tiers' jobs", jobs)
	}
}

func TestARemoteRunOnARunnerOfAnotherOcelWarnsAndRuns(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.RunnerVersion = "9.9.9"

	out, err := runRemotely(t, invocation, writeShop(t), productionDeploy())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "runs ocel 9.9.9") || len(fake.Dispatched) != 1 {
		t.Fatalf("output = %q, dispatched %d, want a warning naming the runner's ocel and the job dispatched", out, len(fake.Dispatched))
	}
}

func TestARemoteTeardownIsShownAsDestroyingNotDeploying(t *testing.T) {
	invocation, _ := loggedIn(t)
	out, err := runRemoteJob(t, t.Context(), invocation, writeShop(t), commands.RemoteRun{
		Command: "ocel preview rm", Kind: runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM,
		Tiers: []environmentv1.Tier{environmentv1.Tier_TIER_PREVIEW}, Preview: &runnerv1.PreviewTarget{Name: "p"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[deploy]") || !strings.Contains(out, "[destroy]") {
		t.Fatalf("output = %q, want the teardown's lines under [destroy]", out)
	}
}

func TestAMultiLineJobFailureKeepsItsLines(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Runner.JobReads = []*runnerv1.Job{{Id: clitest.FakeJobID, State: runnerv1.JobState_JOB_STATE_FAILED, Error: &runnerv1.JobError{Code: "job.run", Message: "credential check failed:\n  ✗ vps: refused"}}}

	_, err := runOnARunner(t, invocation, writeShop(t), "ocel deploy")
	if err == nil || !strings.Contains(err.Error(), "credential check failed:\n  ✗ vps: refused") || strings.Contains(err.Error(), `\n`) {
		t.Fatalf("err = %q, want the failure's lines kept as lines", err)
	}
}
