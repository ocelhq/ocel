package commands

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	cloudVendor        = "Ocel Cloud"
	uploadProgressEach = time.Second
)

type CloudJob struct {
	Command     string
	Tier        environmentv1.Tier
	Kind        cloudv1.JobCommand
	Tag         string
	Preview     *cloudv1.PreviewTarget
	NoWait      bool
	OpenBrowser func(ctx context.Context, url string) error
	Stderr      io.Writer
}

func (i Invocation) RunOnCloud(ctx context.Context, t Target, job CloudJob) (err error) {
	cfg := t.Project
	var ended *cloudv1.Job
	defer func() {
		if job.Stderr != nil {
			i.Console.ReportDeployment(ctx, cfg.Dir, ended.GetReport(), job.Stderr)
		}
	}()
	ctx, running, err := i.Events.Begin(ctx, job.Command, cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)

	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	account := NewConsoleAccount(t.Cloud.ConsoleURL, t.Cloud.Credentials.AccessToken)
	loadingCtx, loading := check.Child(ctx, hostOf(t.Cloud.ConsoleURL), progress.Loading.Title("your organizations"))
	org, err := pickCloudOrganization(loadingCtx, account, cfg.Dir)
	loading.End(err)
	if err != nil {
		check.End(err)
		return err
	}
	identity := cloudIdentity(org, t.Cloud)
	check.Identity(&streamv1.IdentityEvent{Project: cfg.Slug, Tier: job.Tier, Origin: &streamv1.Party{
		Vendor:    identity.GetProvider(),
		Account:   identity.GetAccount(),
		Principal: identity.GetPrincipal(),
		Location:  identity.GetLocation(),
	}})
	if t.Derived != "" {
		check.Say(t.Derived)
	}
	link, err := linkCloudProject(run.ContextWithSpan(ctx, check), check, account, cfg, org)
	check.End(err)
	if err != nil {
		return err
	}

	upload := running.Phase(progressv1.Phase_PHASE_BUILD)
	jobs := cloud.New(t.Cloud.Offer.APIURL, console.NewCloudTokens(account.Client, account.Session, org.ID, cloudv1.Scope_SCOPE_APPS_READ, cloudv1.Scope_SCOPE_APPS_WRITE))
	sourceID, projectPath, err := uploadSource(run.ContextWithSpan(ctx, upload), upload, cfg, jobs)
	upload.End(err)
	if err != nil {
		return err
	}

	phase := running.Phase(jobPhase(job.Kind))
	startingCtx, starting := phase.Child(ctx, cfg.Slug, progress.Title{Started: "Starting the job on Ocel Cloud", Ended: "Started the job on Ocel Cloud"})
	started, err := jobs.StartJob(startingCtx, &cloudv1.StartJobRequest{
		ProjectId:   link.ProjectID,
		Slug:        cfg.Slug,
		Command:     job.Kind,
		SourceId:    sourceID,
		Git:         readGitContext(startingCtx, cfg.Dir),
		Tag:         job.Tag,
		Preview:     job.Preview,
		ProjectPath: projectPath,
	})
	if err != nil {
		starting.End(err)
		return err
	}
	jobURL := JobURL(t.Cloud.ConsoleURL, started.GetId(), org.ID)
	starting.End(nil)
	printedID := terminal.EscapeControls(started.GetId())
	phase.Say(fmt.Sprintf("Job %s · follow it at %s", printedID, jobURL))
	running.SetCloudJob(summarizeJob(started, jobURL))

	if job.NoWait {
		phase.End(nil)
		running.Succeed(fmt.Sprintf("Started job %s on Ocel Cloud, and it runs without this terminal: %s", printedID, jobURL))
		return nil
	}
	ended, err = waitForJob(run.ContextWithSpan(ctx, phase), running, phase, jobs, started, jobURL, cfg.Slug, job)
	return err
}

func waitForJob(ctx context.Context, running *run.Run, phase *run.Span, jobs *cloud.Client, started *cloudv1.Job, jobURL, slug string, job CloudJob) (*cloudv1.Job, error) {
	watchingCtx, watching := phase.Child(ctx, slug, progress.Title{Started: "Waiting for the job on Ocel Cloud", Ended: "Waited for the job on Ocel Cloud"})
	printedID := terminal.EscapeControls(started.GetId())
	var opened string
	resume := func(string) {}
	ended, err := jobs.WaitForJob(watchingCtx, started.GetId(), func(read *cloudv1.Job) {
		running.SetCloudJob(summarizeJob(read, jobURL))
		switch state := read.GetState(); {
		case state == cloudv1.JobState_JOB_STATE_SUCCEEDED:
			resume("answered on Ocel Cloud")
			return
		case cloud.IsJobEnded(state):
			resume("the job stopped waiting")
			return
		case state != cloudv1.JobState_JOB_STATE_INPUT_REQUIRED:
			resume("answered on Ocel Cloud")
			watching.Say(describeJob(read))
			return
		}
		resume("answered on Ocel Cloud")
		input := cmp.Or(read.GetInput(), &cloudv1.JobInput{})
		resume = watching.Hold(&streamv1.WaitingEvent{Url: jobURL, Missing: missingVariables(input.GetVariables()), Input: input})
		if opened == input.GetId() && opened != "" {
			return
		}
		opened = input.GetId()
		if job.OpenBrowser != nil {
			_ = job.OpenBrowser(ctx, jobURL)
		}
	})
	resume("this run stopped watching the job")
	if ended == nil {
		ended = started
	}
	running.SetCloudJob(summarizeJob(ended, jobURL))
	switch {
	case err != nil && ctx.Err() != nil:
		watching.Say("Stopped watching; the job keeps running on Ocel Cloud")
		watching.End(nil)
		phase.End(nil)
		running.Succeed(fmt.Sprintf("Stopped watching job %s, which keeps running on Ocel Cloud: %s", printedID, jobURL))
		return nil, nil
	case err != nil:
		watching.End(err)
		return nil, fmt.Errorf("%w — follow job %s at %s", err, printedID, jobURL)
	}

	switch ended.GetState() {
	case cloudv1.JobState_JOB_STATE_SUCCEEDED:
		for _, line := range jobResultLines(ended.GetResult()) {
			watching.Say(line)
		}
		watching.End(nil)
		phase.End(nil)
		running.Succeed(jobHeadline(ended, job.Kind, slug, jobURL))
		return ended, nil
	case cloudv1.JobState_JOB_STATE_CANCELED:
		err = &clierror.Error{
			Code:  clierror.CodeCloudJobCanceled,
			Cause: fmt.Errorf("job %s was canceled on Ocel Cloud before it finished: %s", printedID, jobURL),
		}
	default:
		failure := ended.GetError()
		var docs string
		if slices.Contains(clierror.CloudCodes(), failure.GetCode()) {
			docs = failure.GetCode()
		}
		err = &clierror.Error{
			Code:     clierror.CodeCloudJobFailed,
			DocsCode: docs,
			Cause:    fmt.Errorf("job %s failed on Ocel Cloud with %s: %s — its log is at %s", printedID, terminal.EscapeControls(cmp.Or(failure.GetCode(), "no error code")), terminal.EscapeControls(cmp.Or(failure.GetMessage(), "no message")), jobURL),
		}
	}
	watching.End(err)
	return ended, err
}

func JobURL(consoleURL, jobID, organizationID string) string {
	return strings.TrimRight(consoleURL, "/") + "/cloud/jobs/" + url.PathEscape(jobID) + "?" + url.Values{"org": {organizationID}}.Encode()
}

func jobPhase(kind cloudv1.JobCommand) progressv1.Phase {
	if kind == cloudv1.JobCommand_JOB_COMMAND_BUILD {
		return progressv1.Phase_PHASE_BUILD
	}
	return progressv1.Phase_PHASE_DEPLOY
}

func jobHeadline(ended *cloudv1.Job, kind cloudv1.JobCommand, slug, jobURL string) string {
	if kind != cloudv1.JobCommand_JOB_COMMAND_BUILD && !deployedAnything(ended) {
		return cmp.Or(terminal.EscapeControls(ended.GetStep()), "Deployed nothing of "+slug+" on Ocel Cloud") + " · job " + jobURL
	}
	switch kind {
	case cloudv1.JobCommand_JOB_COMMAND_BUILD:
		return fmt.Sprintf("Built %s on Ocel Cloud: %s", slug, jobURL)
	case cloudv1.JobCommand_JOB_COMMAND_PREVIEW_UP:
		return fmt.Sprintf("Deployed a preview of %s on Ocel Cloud: %s", slug, jobURL)
	}
	return fmt.Sprintf("Deployed %s on Ocel Cloud: %s", slug, jobURL)
}

func deployedAnything(job *cloudv1.Job) bool {
	result := job.GetResult()
	return job.GetReport() != nil || len(result.GetUrls()) > 0 || result.GetDeploymentId() != ""
}

func describeJob(job *cloudv1.Job) string {
	switch {
	case job.GetStep() != "":
		return terminal.EscapeControls(job.GetStep())
	case job.GetState() == cloudv1.JobState_JOB_STATE_QUEUED:
		return "Waiting for a builder"
	case job.GetPhase() != cloudv1.JobPhase_JOB_PHASE_UNSPECIFIED:
		return "Running: " + strings.ToLower(strings.TrimPrefix(job.GetPhase().String(), "JOB_PHASE_"))
	}
	return "Running"
}

func missingVariables(needed *cloudv1.VariablesNeeded) *streamv1.MissingVariables {
	if needed == nil {
		return nil
	}
	missing := &streamv1.MissingVariables{}
	for _, variable := range needed.GetVariables() {
		missing.Cells = append(missing.Cells, &streamv1.MissingVariable{
			Key:         variable.GetKey(),
			Folder:      variable.GetFolder(),
			Reason:      variable.GetReason(),
			Description: variable.GetDescription(),
			Group:       variable.GetGroup(),
		})
	}
	for _, group := range needed.GetGroups() {
		missing.Groups = append(missing.Groups, &streamv1.MissingGroup{Key: group.GetKey(), Description: group.GetDescription()})
	}
	return missing
}

func jobResultLines(result *cloudv1.JobResult) []string {
	lines := slices.Clone(result.GetUrls())
	for _, built := range result.GetBuilds() {
		lines = append(lines, fmt.Sprintf("Built %s as %s", built.GetApp(), built.GetBuildId()))
	}
	if id := result.GetDeploymentId(); id != "" {
		lines = append(lines, "Deployment "+id)
	}
	for i, line := range lines {
		lines[i] = terminal.EscapeControls(line)
	}
	return lines
}

func summarizeJob(job *cloudv1.Job, jobURL string) *streamv1.CloudJob {
	return &streamv1.CloudJob{Id: job.GetId(), Url: jobURL, State: job.GetState(), Result: job.GetResult(), Error: job.GetError()}
}

func readGitContext(ctx context.Context, dir string) *cloudv1.GitContext {
	source := deployreport.ReadSource(ctx, dir, os.Getenv)
	if source == nil {
		return nil
	}
	git := &cloudv1.GitContext{Commit: source.GetCommit(), Branch: source.GetBranch(), Dirty: source.GetDirty()}
	if pr, err := strconv.ParseUint(previewid.DiscoverPRNumber(), 10, 32); err == nil {
		git.PullRequest = uint32(pr)
	}
	return git
}

func uploadSource(ctx context.Context, phase *run.Span, cfg *project.Project, uploader *cloud.Client) (sourceID, projectPath string, err error) {
	readingCtx, reading := phase.Child(ctx, cfg.Slug, progress.Reading.Title("the source"))
	archive, err := sourcearchive.Pack(readingCtx, cfg.Dir)
	if err != nil {
		reading.End(err)
		return "", "", err
	}
	var total uint64
	for _, file := range archive.Manifest.GetFiles() {
		total += file.GetSize()
	}
	reading.Say(fmt.Sprintf("%s (%s) under %s", english.Count(len(archive.Manifest.GetFiles()), "file"), english.Bytes(total), archive.Root))
	planned, err := uploader.Plan(readingCtx, archive)
	reading.End(err)
	if err != nil {
		return "", "", err
	}

	if len(planned.Missing) == 0 {
		phase.Say("Ocel Cloud already holds every file")
	} else {
		object := fmt.Sprintf("%s (%s) to Ocel Cloud", english.Count(len(planned.Missing), "file"), english.Bytes(planned.TotalBytes))
		uploadingCtx, uploading := phase.Child(ctx, cfg.Slug, progress.Title{Started: "Uploading " + object, Ended: "Uploaded " + object})
		err = uploader.Send(uploadingCtx, archive, planned, reportUpload(uploading))
		uploading.End(err)
		if err != nil {
			return "", "", err
		}
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return "", "", err
	}
	if projectPath, err = filepath.Rel(archive.Root, dir); err != nil {
		return "", "", err
	}
	if projectPath == "." {
		projectPath = ""
	}
	committingCtx, committing := phase.Child(ctx, cfg.Slug, progress.Title{Started: "Committing the source to Ocel Cloud", Ended: "Committed the source to Ocel Cloud"})
	sourceID, err = uploader.Commit(committingCtx, archive.Manifest)
	committing.End(err)
	return sourceID, filepath.ToSlash(projectPath), err
}

func reportUpload(span *run.Span) func(cloud.Progress) {
	var mu sync.Mutex
	last := time.Now()
	return func(p cloud.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if p.SentBytes == 0 || p.SentFiles == p.TotalFiles || time.Since(last) < uploadProgressEach {
			return
		}
		last = time.Now()
		span.Say(fmt.Sprintf("%s of %s · %d of %d files", english.Bytes(p.SentBytes), english.Bytes(p.TotalBytes), p.SentFiles, p.TotalFiles))
	}
}

func pickCloudOrganization(ctx context.Context, account ConsoleAccount, projectDir string) (*console.Organization, error) {
	orgs, err := account.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	link, err := console.ReadLink(projectDir, account.URL)
	if err != nil {
		return nil, err
	}
	if link != nil {
		if org := FindOrganization(orgs, func(org console.Organization) bool { return org.ID == link.OrganizationID }); org != nil {
			return org, nil
		}
		return nil, fmt.Errorf("this directory is linked to %s in an organization you no longer belong to — run `ocel link` to link it again", link.ProjectName)
	}
	if read, err := account.Client.GetSession(ctx, account.Session); err == nil && read != nil {
		if org := FindOrganization(orgs, func(org console.Organization) bool { return org.ID == read.Session.ActiveOrganizationID }); org != nil {
			return org, nil
		}
	}
	if len(orgs) == 1 {
		return &orgs[0], nil
	}
	return nil, clierror.NewInputRequired(
		fmt.Errorf("you belong to %d organizations in %s and none is active — run `ocel link --org <slug>` to pick the one this project deploys under: %s", len(orgs), account.URL, OrganizationSlugs(orgs)),
		"ocel link --org <slug>",
	)
}

func linkCloudProject(ctx context.Context, check *run.Span, account ConsoleAccount, cfg *project.Project, org *console.Organization) (*console.Link, error) {
	if link, err := console.ReadLink(cfg.Dir, account.URL); err != nil || link != nil {
		return link, err
	}
	selected, err := findOrCreateProject(ctx, check, account, cfg.Slug, org)
	if err != nil {
		return nil, err
	}
	link, err := account.WriteLink(cfg.Dir, org, selected)
	if err != nil {
		return nil, err
	}
	check.Say(LinkedMessage(org, selected))
	return &link, nil
}

func findOrCreateProject(ctx context.Context, check *run.Span, account ConsoleAccount, slug string, org *console.Organization) (*consolev1.Project, error) {
	find := func(ctx context.Context) (*consolev1.Project, error) {
		projects, err := account.ListProjects(ctx, org)
		return FindProject(projects, slug), err
	}
	loadingCtx, loading := check.Child(ctx, slug, progress.Loading.Title("the projects in "+org.Name))
	found, err := find(loadingCtx)
	loading.End(err)
	if err != nil || found != nil {
		return found, err
	}
	ctx, creating := check.Child(ctx, slug, progress.Creating.Title("the project in "+org.Name))
	created, err := account.Client.CreateProject(ctx, account.Session, slug, slug)
	if console.IsConflict(err) {
		created, err = find(ctx)
		if err == nil && created == nil {
			err = errors.New("the console reports the project exists but does not list it")
		}
	}
	if err != nil {
		err = fmt.Errorf("create the project %s in %s: %w", slug, org.Name, err)
	}
	creating.End(err)
	return created, err
}

func cloudIdentity(org *console.Organization, cloud *CloudTarget) *contractv1.Identity {
	return &contractv1.Identity{Provider: cloudVendor, Account: org.Name, Principal: cloud.Credentials.Email, Location: hostOf(cloud.ConsoleURL)}
}

func hostOf(consoleURL string) string {
	if u, err := url.Parse(consoleURL); err == nil && u.Host != "" {
		return u.Host
	}
	return consoleURL
}

const noWaitFlag = "no-wait"

func AddNoWaitFlag(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVar(into, noWaitFlag, false, "On Ocel Cloud, start the job, print its link and return without waiting for it to end")
}

func RefuseNoWait() error {
	return &clierror.Error{Code: clierror.CodeUsage, Cause: fmt.Errorf("--%s leaves a job running on Ocel Cloud, and this project does not run on Ocel Cloud: it runs here, with no job to leave running", noWaitFlag)}
}
