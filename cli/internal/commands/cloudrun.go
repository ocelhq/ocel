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
	OpenBrowser func(url string) error
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
	client := console.New(t.Cloud.ConsoleURL)
	session := t.Cloud.Credentials.AccessToken
	org, err := pickCloudOrganization(ctx, client, session, cfg.Dir, t.Cloud.ConsoleURL)
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
	link, err := linkCloudProject(ctx, check, client, session, cfg, org, t.Cloud.ConsoleURL)
	check.End(err)
	if err != nil {
		return err
	}

	upload := running.Phase(progressv1.Phase_PHASE_BUILD)
	jobs := cloud.New(t.Cloud.Offer.APIURL, console.NewCloudTokens(client, session, org.ID, cloudv1.Scope_SCOPE_APPS_READ, cloudv1.Scope_SCOPE_APPS_WRITE))
	sourceID, projectPath, err := uploadSource(ctx, upload, cfg, jobs)
	upload.End(err)
	if err != nil {
		return err
	}

	phase := running.Phase(jobPhase(job.Kind))
	starting := phase.Child(cfg.Slug, progress.Title{Started: "Starting the job on Ocel Cloud", Ended: "Started the job on Ocel Cloud"})
	started, err := jobs.StartJob(ctx, &cloudv1.StartJobRequest{
		ProjectId:   link.ProjectID,
		Slug:        cfg.Slug,
		Command:     job.Kind,
		SourceId:    sourceID,
		Git:         readGitContext(cfg.Dir),
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
	phase.Say(fmt.Sprintf("Job %s · follow it at %s", started.GetId(), jobURL))
	running.SetCloudJob(summarizeJob(started, jobURL))

	if job.NoWait {
		phase.End(nil)
		running.Succeed(fmt.Sprintf("Started job %s on Ocel Cloud, and it runs without this terminal: %s", started.GetId(), jobURL))
		return nil
	}
	ended, err = waitForJob(ctx, running, phase, jobs, started, jobURL, cfg.Slug, job)
	return err
}

func waitForJob(ctx context.Context, running *run.Run, phase *run.Span, jobs *cloud.Client, started *cloudv1.Job, jobURL, slug string, job CloudJob) (*cloudv1.Job, error) {
	watching := phase.Child(slug, progress.Title{Started: "Waiting for the job on Ocel Cloud", Ended: "Waited for the job on Ocel Cloud"})
	var opened string
	resume := func(string) {}
	ended, err := jobs.WaitForJob(ctx, started.GetId(), func(read *cloudv1.Job) {
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
			_ = job.OpenBrowser(jobURL)
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
		running.Succeed(fmt.Sprintf("Stopped watching job %s, which keeps running on Ocel Cloud: %s", started.GetId(), jobURL))
		return nil, nil
	case err != nil:
		watching.End(err)
		return nil, fmt.Errorf("%w — follow job %s at %s", err, started.GetId(), jobURL)
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
			Cause: fmt.Errorf("job %s was canceled on Ocel Cloud before it finished: %s", started.GetId(), jobURL),
		}
	default:
		failure := ended.GetError()
		err = &clierror.Error{
			Code:  clierror.CodeCloudJobFailed,
			Cause: fmt.Errorf("job %s failed on Ocel Cloud with %s: %s — its log is at %s", started.GetId(), cmp.Or(failure.GetCode(), "no error code"), cmp.Or(failure.GetMessage(), "no message"), jobURL),
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
		return cmp.Or(ended.GetStep(), "Deployed nothing of "+slug+" on Ocel Cloud") + " · job " + jobURL
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
		return job.GetStep()
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
	return lines
}

func summarizeJob(job *cloudv1.Job, jobURL string) *streamv1.CloudJob {
	return &streamv1.CloudJob{Id: job.GetId(), Url: jobURL, State: job.GetState(), Result: job.GetResult(), Error: job.GetError()}
}

func readGitContext(dir string) *cloudv1.GitContext {
	source := deployreport.ReadSource(dir, os.Getenv)
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
	reading := phase.Child(cfg.Slug, progress.Reading.Title("the source"))
	archive, err := sourcearchive.Pack(cfg.Dir)
	if err == nil && len(archive.Manifest.GetFiles()) == 0 {
		err = fmt.Errorf("%s holds no file to upload once what is ignored is left out", cfg.Dir)
	}
	if err != nil {
		reading.End(err)
		return "", "", err
	}
	var total uint64
	for _, file := range archive.Manifest.GetFiles() {
		total += file.GetSize()
	}
	reading.Say(fmt.Sprintf("%s (%s) under %s", english.Count(len(archive.Manifest.GetFiles()), "file"), english.Bytes(total), archive.Root))
	planned, err := uploader.Plan(ctx, archive)
	reading.End(err)
	if err != nil {
		return "", "", err
	}

	if len(planned.Missing) == 0 {
		phase.Say("Ocel Cloud already holds every file")
	} else {
		object := fmt.Sprintf("%s (%s) to Ocel Cloud", english.Count(len(planned.Missing), "file"), english.Bytes(planned.TotalBytes))
		uploading := phase.Child(cfg.Slug, progress.Title{Started: "Uploading " + object, Ended: "Uploaded " + object})
		err = uploader.Send(ctx, archive, planned, reportUpload(uploading))
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
	sourceID, err = uploader.Commit(ctx, archive.Manifest)
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

func pickCloudOrganization(ctx context.Context, client *console.Client, session, projectDir, consoleURL string) (*console.Organization, error) {
	orgs, err := client.ListOrganizations(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("list your organizations in %s: %w", consoleURL, err)
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("you belong to no organization in %s yet — create one there first", consoleURL)
	}
	find := func(id string) *console.Organization {
		for i := range orgs {
			if orgs[i].ID == id {
				return &orgs[i]
			}
		}
		return nil
	}
	link, err := console.ReadLink(projectDir, consoleURL)
	if err != nil {
		return nil, err
	}
	if link != nil {
		if org := find(link.OrganizationID); org != nil {
			return org, nil
		}
		return nil, fmt.Errorf("this directory is linked to %s in an organization you no longer belong to — run `ocel link` to link it again", link.ProjectName)
	}
	if read, err := client.GetSession(ctx, session); err == nil && read != nil {
		if org := find(read.Session.ActiveOrganizationID); org != nil {
			return org, nil
		}
	}
	if len(orgs) == 1 {
		return &orgs[0], nil
	}
	slugs := make([]string, 0, len(orgs))
	for _, org := range orgs {
		slugs = append(slugs, org.Slug)
	}
	return nil, clierror.NewInputRequired(
		fmt.Errorf("you belong to %d organizations in %s and none is active — run `ocel link --org <slug>` to pick the one this project deploys under: %s", len(orgs), consoleURL, strings.Join(slugs, ", ")),
		"ocel link --org <slug>",
	)
}

func linkCloudProject(ctx context.Context, check *run.Span, client *console.Client, session string, cfg *project.Project, org *console.Organization, consoleURL string) (*console.Link, error) {
	if link, err := console.ReadLink(cfg.Dir, consoleURL); err != nil || link != nil {
		return link, err
	}
	if err := client.SetActiveOrganization(ctx, session, org.ID); err != nil {
		return nil, fmt.Errorf("make %s your active organization in %s: %w", org.Name, consoleURL, err)
	}
	selected, err := findOrCreateProject(ctx, check, client, session, cfg.Slug, org)
	if err != nil {
		return nil, err
	}
	link := console.Link{
		APIURL:         consoleURL,
		OrganizationID: org.ID,
		ProjectID:      selected.GetId(),
		ProjectName:    selected.GetName(),
	}
	if err := console.WriteLink(cfg.Dir, link); err != nil {
		return nil, err
	}
	check.Say(fmt.Sprintf("Linked this directory to %s (%s)", selected.GetSlug(), org.Name))
	return &link, nil
}

func findOrCreateProject(ctx context.Context, check *run.Span, client *console.Client, session, slug string, org *console.Organization) (*consolev1.Project, error) {
	find := func() (*consolev1.Project, error) {
		projects, err := client.ListProjects(ctx, session)
		if err != nil {
			return nil, fmt.Errorf("list the projects in %s: %w", org.Name, err)
		}
		for _, p := range projects {
			if p.GetSlug() == slug {
				return p, nil
			}
		}
		return nil, nil
	}
	if found, err := find(); err != nil || found != nil {
		return found, err
	}
	creating := check.Child(slug, progress.Creating.Title("the project in "+org.Name))
	created, err := client.CreateProject(ctx, session, slug, slug)
	if console.IsConflict(err) {
		created, err = find()
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
	return fmt.Errorf("--%s leaves a job running on Ocel Cloud, and this project does not run on Ocel Cloud: it runs here, with no job to leave running", noWaitFlag)
}
