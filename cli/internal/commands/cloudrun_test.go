package commands_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func runOnCloud(t *testing.T, invocation commands.Invocation, root, command string) (string, error) {
	t.Helper()
	return runJobOnCloud(t, t.Context(), invocation, root, commands.CloudJob{Command: command, Tier: environmentv1.Tier_TIER_PRODUCTION, Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY})
}

func runJobOnCloud(t *testing.T, ctx context.Context, invocation commands.Invocation, root string, job commands.CloudJob) (string, error) {
	t.Helper()
	target, err := invocation.ResolveTarget(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !target.IsCloud() {
		t.Fatal("the target is not Ocel Cloud")
	}
	var out bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	err = invocation.RunOnCloud(ctx, target, job)
	if closeErr := invocation.Events.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return out.String(), err
}

func writeShop(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "shop")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "console.log('hi')")
	return root
}

func TestTheFirstOcelCloudRunLinksAConsoleProjectUploadsTheSourceAndRunsAJob(t *testing.T) {
	t.Setenv(previewid.PRNumberEnvVar, "")
	t.Setenv("GITHUB_REF", "")
	invocation, fake := loggedIn(t)
	root := writeShop(t)
	clitest.WriteFile(t, filepath.Join(root, ".env"), "SECRET=1")
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED, Result: &cloudv1.JobResult{
		Urls:         []string{"https://shop.ocel.app"},
		DeploymentId: "dep_1",
	}}}

	out, err := runOnCloud(t, invocation, root, "ocel deploy")
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
	jobURL := fake.ConsoleURL + "/cloud/jobs/" + clitest.FakeCloudJobID + "?org=org_1"
	if !strings.Contains(out, jobURL) {
		t.Fatalf("output = %q, want the console's job page %s", out, jobURL)
	}
	if !strings.Contains(out, "https://shop.ocel.app") || !strings.Contains(out, "dep_1") {
		t.Fatalf("output = %q, want the job's result", out)
	}
	if len(fake.Created) != 1 || fake.Created[0].GetSlug() != "shop" {
		t.Fatalf("created %v, want the console project shop", fake.Created)
	}
	link, err := console.ReadLink(root, fake.ConsoleURL)
	if err != nil || link == nil || link.OrganizationID != "org_1" || link.ProjectName != "shop" {
		t.Fatalf("link = %+v, err = %v, want this directory linked to shop", link, err)
	}
	if got := fake.CommittedPaths(); !slices.Equal(got, []string{"index.js", "ocel.json"}) {
		t.Fatalf("committed %q, want the source without .env or .ocel", got)
	}
	want := &cloudv1.StartJobRequest{ProjectId: link.ProjectID, Slug: "shop", Command: cloudv1.JobCommand_JOB_COMMAND_DEPLOY, SourceId: clitest.FakeCloudSourceID}
	if len(fake.Started) != 1 || !proto.Equal(fake.Started[0], want) {
		t.Fatalf("started %v, want %v", fake.Started, want)
	}
	for _, bearer := range fake.CloudBearers {
		if bearer != "Bearer "+clitest.FakeCloudToken {
			t.Fatalf("Ocel Cloud saw %q, want the token the console issued", bearer)
		}
	}

	again, err := runOnCloud(t, newInvocationLoggedInTo(fake), root, "ocel deploy")
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

func TestTheFirstOcelCloudRunNamesItsReadsOfTheConsoleAndTheCommitOfItsSource(t *testing.T) {
	invocation, fake := loggedIn(t)
	root := writeShop(t)
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runOnCloud(t, invocation, root, "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	for _, named := range []struct{ phase, ended string }{
		{"check", "Loaded your organizations"},
		{"check", "Loaded the projects in Acme Inc"},
		{"build", "Committed the source to Ocel Cloud"},
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

func TestAnOcelCloudJobsLinkIsALineOfItsOwnThatNoFoldedGroupHides(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{GitHubActions: true, ColorAsked: terminal.ColorNever})
	}
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	link := "INFO  [deploy] Job " + clitest.FakeCloudJobID + " · follow it at " + fake.ConsoleURL + "/cloud/jobs/" + clitest.FakeCloudJobID + "?org=org_1" + "\n"
	at := strings.Index(out, link)
	if at < 0 || strings.Count(out[:at], "::group::") != strings.Count(out[:at], "::endgroup::") {
		t.Fatalf("output = %q, want %q outside every group", out, link)
	}
}

func TestAnOcelCloudJobCarriesTheGitContextReadOnTheLaptop(t *testing.T) {
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

	if _, err := runOnCloud(t, invocation, root, "ocel deploy"); err != nil {
		t.Fatal(err)
	}
	git := fake.Started[0].GetGit()
	if len(git.GetCommit()) != 40 || git.GetBranch() != "feat/cart" || !git.GetDirty() || git.GetPullRequest() != 12 {
		t.Fatalf("git = %v, want the commit, feat/cart, dirty and pull request 12", git)
	}
}

func TestAnOcelCloudJobNamesWhereTheProjectSitsInTheUploadedSource(t *testing.T) {
	invocation, fake := loggedIn(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, "apps", "web")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "web"}`)
	clitest.WriteFile(t, filepath.Join(repo, "package.json"), `{}`)

	if _, err := runOnCloud(t, invocation, root, "ocel deploy"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Started[0].GetProjectPath(); got != "apps/web" {
		t.Fatalf("project path = %q, want apps/web", got)
	}
	if got := fake.CommittedPaths(); !slices.Equal(got, []string{"apps/web/ocel.json", "package.json"}) {
		t.Fatalf("committed %q, want the whole repository", got)
	}
}

func TestAnOcelCloudJobThatFailsFailsTheRunWithItsErrorAndLink(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_FAILED, Error: &cloudv1.JobError{Code: "build.failed", Message: "app web failed to build"}}}

	out, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudJobFailed {
		t.Fatalf("err = %v, want %s", err, clierror.CodeCloudJobFailed)
	}
	for _, want := range []string{"build.failed", "app web failed to build", fake.ConsoleURL + "/cloud/jobs/" + clitest.FakeCloudJobID + "?org=org_1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q named", err, want)
		}
	}
	if !strings.Contains(out, "app web failed to build") {
		t.Fatalf("output = %q, want the failure shown", out)
	}
}

func TestWhatOcelCloudSaysOfAJobReachesTheTerminalWithItsControlCharactersEscaped(t *testing.T) {
	const clear = "\x1b[2J"
	t.Run("a job that succeeds", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.JobReads = []*cloudv1.Job{
			{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_RUNNING, Step: "Building" + clear},
			{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED, Result: &cloudv1.JobResult{
				Urls:         []string{"https://shop.ocel.app" + clear},
				Builds:       []*cloudv1.AppBuild{{App: "web" + clear, BuildId: "b1" + clear}},
				DeploymentId: "d1" + clear,
			}},
		}
		out, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "\x1b") || !strings.Contains(out, `Building\u001b[2J`) || !strings.Contains(out, `https://shop.ocel.app\u001b[2J`) {
			t.Fatalf("output = %q, want each escape Ocel Cloud sent shown escaped", out)
		}
	})

	t.Run("a job that fails", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_FAILED, Error: &cloudv1.JobError{Code: "build.failed" + clear, Message: "app web failed" + clear}}}
		_, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
		if err == nil || strings.Contains(err.Error(), "\x1b") || !strings.Contains(err.Error(), `app web failed\u001b[2J`) {
			t.Fatalf("err = %q, want the failure with its escapes shown escaped", err)
		}
	})
}

func TestAnOcelCloudJobThatFailsWithACodeOfOcelCloudsOwnLinksThatCodesPage(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_FAILED, Error: &cloudv1.JobError{Code: "cloud.builder", Message: "the builder stopped"}}}

	_, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeCloudJobFailed || got.GetDocsUrl() != clierror.FormatErrorPage("cloud.builder") {
		t.Fatalf("run error = %v, want %s linking cloud.builder's page", got, clierror.CodeCloudJobFailed)
	}
}

func jobReport(outcome consolev1.DeploymentOutcome) *consolev1.Deployment {
	report := &consolev1.Deployment{
		Id:          "4bf92f3577b34da6a3ce929d0e0e4736",
		Kind:        consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY,
		Outcome:     outcome,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		Provider:    &consolev1.Provider{Name: commands.CloudProviderID},
		Target:      "ocel/cloud",
		StartedAt:   timestamppb.New(time.Unix(1_800_000_000, 0)),
		FinishedAt:  timestamppb.New(time.Unix(1_800_000_060, 0)),
		Trigger:     &consolev1.Trigger{Kind: consolev1.TriggerKind_TRIGGER_KIND_CLI},
	}
	if outcome == consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED {
		report.Promotion = &consolev1.Promotion{Id: "7068ee76eeff4094730614560752fc7b", Seq: 1_800_000_060}
	} else {
		report.Error = "app web failed to build"
	}
	return report
}

func TestAnOcelCloudJobThatEndsWithADeploymentReportsItToTheLinkedConsoleProject(t *testing.T) {
	for _, tc := range []struct {
		state   cloudv1.JobState
		outcome consolev1.DeploymentOutcome
	}{
		{cloudv1.JobState_JOB_STATE_SUCCEEDED, consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED},
		{cloudv1.JobState_JOB_STATE_FAILED, consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_FAILED},
	} {
		t.Run(tc.state.String(), func(t *testing.T) {
			invocation, fake := loggedIn(t)
			report := jobReport(tc.outcome)
			fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: tc.state, Report: report, Result: &cloudv1.JobResult{DeploymentId: report.GetId()}}}
			var stderr bytes.Buffer

			_, _ = runJobOnCloud(t, t.Context(), invocation, writeShop(t), commands.CloudJob{Command: "ocel deploy", Tier: environmentv1.Tier_TIER_PRODUCTION, Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY, Stderr: &stderr})

			reports := fake.ReportedDeployments()
			if len(reports) != 1 || stderr.Len() != 0 {
				t.Fatalf("the console received %d reports, stderr=%q, want the job's deployment reported once", len(reports), stderr.String())
			}
			if reports[0].GetProjectId() != fake.Projects[0].GetId() || !proto.Equal(reports[0].GetDeployment(), report) {
				t.Errorf("reported %v, want the job's deployment in the linked project %s", reports[0], fake.Projects[0].GetId())
			}
		})
	}
}

func TestAnOcelCloudJobThatEndsWithNoDeploymentReportsNothing(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}
	var stderr bytes.Buffer

	if _, err := runJobOnCloud(t, t.Context(), invocation, writeShop(t), commands.CloudJob{Command: "ocel build", Kind: cloudv1.JobCommand_JOB_COMMAND_BUILD, Stderr: &stderr}); err != nil {
		t.Fatal(err)
	}
	if len(fake.ReportedDeployments()) != 0 || stderr.Len() != 0 {
		t.Errorf("reported %v, stderr=%q, want nothing from a job that made no deployment", fake.ReportedDeployments(), stderr.String())
	}
}

func TestAnOcelCloudDeployThatDeployedNothingEndsWithTheJobsNoticeNotDeployed(t *testing.T) {
	invocation, fake := loggedIn(t)
	notice := `Nothing to deploy: shop declares no apps or resources, and no app is detected at the project root: name the app to deploy under "apps"`
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED, Step: notice}}

	out, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Deployed shop") {
		t.Fatalf("output = %q, want no claim of a deployment from a job that deployed nothing", out)
	}
	if !strings.Contains(out, notice+" · job "+fake.ConsoleURL+"/cloud/jobs/"+clitest.FakeCloudJobID+"?org=org_1") {
		t.Fatalf("output = %q, want the job's notice and its link", out)
	}
}

func TestAnOcelCloudJobThatIsCanceledFailsTheRun(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_CANCELED}}

	_, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudJobCanceled {
		t.Fatalf("err = %v, want %s", err, clierror.CodeCloudJobCanceled)
	}
}

func TestAnOcelCloudRunWithNoWaitReturnsOnceTheJobStarts(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.OnJobRead = func(*cloudv1.Job) { t.Error("read the job of a run that does not wait") }

	out, err := runJobOnCloud(t, t.Context(), invocation, writeShop(t), commands.CloudJob{Command: "ocel build", Kind: cloudv1.JobCommand_JOB_COMMAND_BUILD, NoWait: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, clitest.FakeCloudJobID) || !strings.Contains(out, fake.ConsoleURL+"/cloud/jobs/"+clitest.FakeCloudJobID+"?org=org_1") {
		t.Fatalf("output = %q, want the job id and its link", out)
	}
	if len(fake.Started) != 1 {
		t.Fatalf("started %d jobs, want one", len(fake.Started))
	}
}

func TestInterruptingAnOcelCloudRunStopsWatchingAndLeavesTheJobRunning(t *testing.T) {
	invocation, fake := loggedIn(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_RUNNING, Phase: cloudv1.JobPhase_JOB_PHASE_BUILD, Step: "Building web"}}
	fake.OnJobRead = func(*cloudv1.Job) { cancel() }

	out, err := runJobOnCloud(t, ctx, invocation, writeShop(t), commands.CloudJob{Command: "ocel deploy", Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatalf("err = %v, want stopping to watch to succeed", err)
	}
	if !strings.Contains(out, "keeps running") || !strings.Contains(out, fake.ConsoleURL+"/cloud/jobs/"+clitest.FakeCloudJobID+"?org=org_1") {
		t.Fatalf("output = %q, want the job named still running, with its link", out)
	}
	if len(fake.Canceled) != 0 {
		t.Fatalf("canceled %v, want the job left running", fake.Canceled)
	}
}

func needingInput(id string, need *cloudv1.JobInput) *cloudv1.Job {
	need.Id = id
	return &cloudv1.Job{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_INPUT_REQUIRED, Input: need}
}

func needingValues(id string, keys ...string) *cloudv1.Job {
	needed := &cloudv1.VariablesNeeded{Tier: environmentv1.Tier_TIER_PRODUCTION}
	for _, key := range keys {
		needed.Variables = append(needed.Variables, &cloudv1.NeededVariable{Key: key})
	}
	return needingInput(id, &cloudv1.JobInput{Need: &cloudv1.JobInput_Variables{Variables: needed}})
}

func TestAnOcelCloudJobThatNeedsInputOpensItsPageOncePerInputAndKeepsWaiting(t *testing.T) {
	invocation, fake := loggedIn(t)
	values := needingValues("in_1", "DATABASE_URL", "STRIPE_KEY")
	running := &cloudv1.Job{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_RUNNING}
	confirm := needingInput("in_2", &cloudv1.JobInput{Need: &cloudv1.JobInput_Confirmation{Confirmation: &cloudv1.Confirmation{Question: "Continue?"}}})
	fake.JobReads = []*cloudv1.Job{values, values, running, values, confirm, {Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}
	var opened []string

	out, err := runJobOnCloud(t, t.Context(), invocation, writeShop(t), commands.CloudJob{
		Command:     "ocel deploy",
		Kind:        cloudv1.JobCommand_JOB_COMMAND_DEPLOY,
		OpenBrowser: func(url string) error { opened = append(opened, url); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	jobURL := fake.ConsoleURL + "/cloud/jobs/" + clitest.FakeCloudJobID + "?org=org_1"
	if !slices.Equal(opened, []string{jobURL, jobURL}) {
		t.Fatalf("opened %q, want the job page once for the values and once for the question", opened)
	}
	for _, want := range []string{"2 variables are not ready", "Ocel Cloud asks: Continue?"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want %q said", out, want)
		}
	}
	if resumed := strings.Count(out, "Resumed — answered on Ocel Cloud"); resumed != 3 {
		t.Errorf("output = %q, want the run resumed each time the job stopped waiting, 3 times, not %d", out, resumed)
	}
}

func TestAnOcelCloudJobThatNeedsInputWithNoBrowserSaysWhereAndKeepsWaiting(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.JobReads = []*cloudv1.Job{needingValues("in_1", "DATABASE_URL"), {Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runJobOnCloud(t, t.Context(), invocation, writeShop(t), commands.CloudJob{Command: "ocel deploy", Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY})
	if err != nil {
		t.Fatal(err)
	}
	asked := strings.Index(out, "✗ DATABASE_URL")
	page := strings.Index(out, "Fill them in on Ocel Cloud at:\n\n    "+fake.ConsoleURL+"/cloud/jobs/"+clitest.FakeCloudJobID+"?org=org_1"+"\n")
	waited := strings.Index(out, "Waited for the job")
	if asked < 0 || page < asked || waited < page {
		t.Fatalf("output = %q, want the values needed and where to enter them, before the wait ends", out)
	}
}

func TestAnOcelCloudJobWaitingForInputHoldsTheRunOnItsPageUntilTheInputIsAnswered(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	values := needingValues("in_1", "DATABASE_URL")
	fake.JobReads = []*cloudv1.Job{values, {Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}

	out, err := runJobOnCloud(t, t.Context(), invocation, writeShop(t), commands.CloudJob{Command: "ocel deploy", Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY})
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
		Url:     fake.ConsoleURL + "/cloud/jobs/" + clitest.FakeCloudJobID + "?org=org_1",
		Missing: &streamv1.MissingVariables{Cells: []*streamv1.MissingVariable{{Key: "DATABASE_URL"}}},
		Input:   values.GetInput(),
	}
	if !proto.Equal(waiting, want) {
		t.Fatalf("waiting = %v, want %v", waiting, want)
	}
	if resumed.GetReason() != "answered on Ocel Cloud" {
		t.Fatalf("resumed = %v, want the run resumed once the input is answered", resumed)
	}
}

func TestARunThatStopsWatchingAJobWaitingForInputResumesTheWait(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.JobReads = []*cloudv1.Job{needingValues("in_1", "DATABASE_URL")}
	reads := 0
	fake.OnJobRead = func(*cloudv1.Job) {
		if reads++; reads == 2 {
			cancel()
		}
	}

	out, err := runJobOnCloud(t, ctx, invocation, writeShop(t), commands.CloudJob{Command: "ocel deploy", Kind: cloudv1.JobCommand_JOB_COMMAND_DEPLOY})
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

func TestTheJSONSummaryOfAnOcelCloudRunNamesTheJob(t *testing.T) {
	invocation, fake := loggedIn(t)
	invocation.Presentation = clitest.ResolveJSONPresentation
	fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED, Result: &cloudv1.JobResult{Urls: []string{"https://shop.ocel.app"}}}}

	out, err := runOnCloud(t, invocation, writeShop(t), "ocel deploy")
	if err != nil {
		t.Fatal(err)
	}
	events := clitest.RunEvents(t, out)
	summary := events[len(events)-1].GetSummary()
	want := &streamv1.CloudJob{
		Id:     clitest.FakeCloudJobID,
		Url:    fake.ConsoleURL + "/cloud/jobs/" + clitest.FakeCloudJobID + "?org=org_1",
		State:  cloudv1.JobState_JOB_STATE_SUCCEEDED,
		Result: &cloudv1.JobResult{Urls: []string{"https://shop.ocel.app"}},
	}
	if !summary.GetSuccess() || !proto.Equal(summary.GetCloudJob(), want) {
		t.Fatalf("summary = %v, want a success naming %v", summary, want)
	}
}

func newInvocationLoggedInTo(fake *clitest.FakeOcelCloud) commands.Invocation {
	invocation := clitest.NewInvocation()
	fake.LogIn(&invocation)
	return invocation
}

func TestAnOcelCloudRunSelectsTheConsoleProjectWithTheProjectsSlug(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Projects = []*consolev1.Project{{Id: "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79", OrganizationId: "org_1", Name: "Shop", Slug: "shop"}}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)

	if _, err := runOnCloud(t, invocation, root, "ocel build"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Created) != 0 {
		t.Fatalf("created %v, want the existing project selected", fake.Created)
	}
	if link, _ := console.ReadLink(root, fake.ConsoleURL); link == nil || link.ProjectID != "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79" {
		t.Fatalf("link = %+v, want the existing project", link)
	}
}

func TestAnOcelCloudRunDeploysUnderTheActiveOrganizationOrAsksForOne(t *testing.T) {
	two := []console.Organization{{ID: "org_1", Name: "Acme Inc", Slug: "acme"}, {ID: "org_2", Name: "Globex", Slug: "globex"}}

	t.Run("the active organization", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations, fake.ActiveOrg = two, "org_2"
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		out, _ := runOnCloud(t, invocation, root, "ocel deploy")
		if !strings.Contains(out, "Globex") {
			t.Fatalf("output = %q, want Globex named", out)
		}
		if link, _ := console.ReadLink(root, fake.ConsoleURL); link == nil || link.OrganizationID != "org_2" {
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
		if err := console.WriteLink(root, console.Link{APIURL: fake.ConsoleURL, OrganizationID: "org_1", ProjectID: clitest.FixtureConsoleProjectID, ProjectName: "shop"}); err != nil {
			t.Fatal(err)
		}
		fake.JobReads = []*cloudv1.Job{{Id: clitest.FakeCloudJobID, State: cloudv1.JobState_JOB_STATE_SUCCEEDED}}
		out, err := runOnCloud(t, invocation, root, "ocel deploy")
		if err != nil {
			t.Fatal(err)
		}
		if len(fake.ActivatedOrgs) != 0 {
			t.Fatalf("activated %v, want the console's active organization left alone", fake.ActivatedOrgs)
		}
		if len(fake.TokenOrgs) == 0 || slices.ContainsFunc(fake.TokenOrgs, func(org string) bool { return org != "org_1" }) {
			t.Fatalf("tokens issued for %v, want each for org_1, the linked organization", fake.TokenOrgs)
		}
		if !strings.Contains(out, "/cloud/jobs/"+clitest.FakeCloudJobID+"?org=org_1") {
			t.Fatalf("output = %q, want the job page naming org_1", out)
		}
		if want := []string{"cloud.apps.read", "cloud.apps.write"}; len(fake.TokenScopes) == 0 || slices.ContainsFunc(fake.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
			t.Fatalf("tokens issued with scopes %v, want each with exactly %v", fake.TokenScopes, want)
		}
	})

	t.Run("none active", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations = two
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		_, err := runOnCloud(t, invocation, root, "ocel deploy")
		var coded *clierror.Error
		if !errors.As(err, &coded) || coded.Code != clierror.CodeInputRequired || !strings.Contains(err.Error(), "ocel link --org") {
			t.Fatalf("err = %v, want ocel link --org asked for", err)
		}
		if len(fake.Created) != 0 || fake.Committed != nil || len(fake.Started) != 0 {
			t.Fatal("linked, uploaded or started a job without an organization")
		}
	})
}
