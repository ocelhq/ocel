package commands

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/confirmation"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/endpointcall"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/runnerclient"
	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/pkg/scope"
)

const (
	cloudVendor        = "Ocel Cloud"
	uploadProgressEach = time.Second
	dispatchAttempts   = 4
	firstDispatchWait  = 500 * time.Millisecond
)

var remoteRunProcedures = []string{
	runnerv1connect.RunnerServiceListMissingBlobsProcedure,
	runnerv1connect.RunnerServiceCommitSourceProcedure,
	runnerv1connect.RunnerServiceGetJobProcedure,
	runnerv1connect.RunnerServiceListJobEventsProcedure,
	runnerv1connect.RunnerServiceAnswerConfirmationProcedure,
	runnerv1connect.RunnerServiceCancelJobProcedure,
}

type RemoteRun struct {
	Command    string
	Kind       runnerv1.JobCommand
	Tiers      []environmentv1.Tier
	Tag        string
	Preview    *runnerv1.PreviewTarget
	TierValues bool
	Yes        bool
	NoWait     bool
	Consent    *RemoteConsent
}

type RemoteConsent struct {
	Ask      func(ctx context.Context, span *run.Span) (bool, error)
	Declined string
	Done     string
}

type remoteRunner struct {
	id       string
	name     string
	endpoint string
	url      string
}

type remoteJob struct {
	tier           environmentv1.Tier
	runner         remoteRunner
	client         *runnerclient.Client
	supplying      *runnerclient.Client
	variableTokens endpointcall.TokenSource
	jobURL         string
}

type remoteSession struct {
	running *run.Run
	account ConsoleAccount
	org     *console.Organization
	link    *console.Link
	cfg     *project.Project
	run     RemoteRun
	browser func(ctx context.Context, url string) error
}

type uploadedSource struct {
	id          string
	projectPath string
}

func (t Target) IsRemote(remoteFlag bool) bool {
	return t.IsCloud() || remoteFlag
}

func (i Invocation) RunRemote(ctx context.Context, t Target, stdin io.Reader, r RemoteRun) (err error) {
	if i.Job() != nil {
		return &clierror.Error{Code: clierror.CodeUsage, Cause: fmt.Errorf("`%s --remote` runs inside a job, which is itself remote: drop --remote", r.Command)}
	}
	cfg := t.Project
	consoleURL, credentials, err := i.readRemoteLogin(t)
	if err != nil {
		return err
	}
	ctx, running, err := i.Events.Begin(ctx, r.Command, cfg.Dir)
	if err != nil {
		return err
	}
	defer running.End(&err)

	session := &remoteSession{running: running, account: NewConsoleAccount(consoleURL, credentials.AccessToken), cfg: cfg, run: r}
	if i.OpenBrowser != nil && i.IsBrowserReachable(stdin) {
		session.browser = i.OpenBrowser
	}
	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	jobs, err := i.prepareRemote(ctx, check, session, t, credentials.Email)
	if err == nil && r.Consent != nil {
		var granted bool
		if granted, err = r.Consent.Ask(ctx, check); err == nil && !granted {
			check.End(nil)
			running.Succeed(r.Consent.Declined)
			return nil
		}
	}
	check.End(err)
	if err != nil {
		return err
	}

	upload := running.Phase(progressv1.Phase_PHASE_BUILD)
	sources := map[string]uploadedSource{}
	for _, job := range jobs {
		if _, uploaded := sources[job.runner.id]; uploaded {
			continue
		}
		var source uploadedSource
		source.id, source.projectPath, err = uploadSource(run.ContextWithSpan(ctx, upload), upload, cfg, job.runner.name, job.client)
		if err != nil {
			upload.End(err)
			return err
		}
		sources[job.runner.id] = source
	}
	upload.End(nil)

	phase := running.Phase(jobPhase(r.Kind))
	for at, job := range jobs {
		if err := i.dispatch(ctx, session, job, phase, sources[job.runner.id]); err != nil {
			return err
		}
		if ctx.Err() != nil && at < len(jobs)-1 {
			return &clierror.Error{Code: clierror.CodeInterrupted, Cause: fmt.Errorf("stopped before %s was dispatched, so nothing runs there", tierNames(jobs[at+1:]))}
		}
	}
	phase.End(nil)
	if r.Consent != nil && r.Consent.Done != "" && !r.NoWait && ctx.Err() == nil {
		running.Succeed(r.Consent.Done)
	}
	return nil
}

func tierNames(jobs []*remoteJob) string {
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		names = append(names, readiness.TierName(job.tier))
	}
	return english.And(names)
}

func (i Invocation) prepareRemote(ctx context.Context, check *run.Span, session *remoteSession, t Target, email string) ([]*remoteJob, error) {
	cfg, account := session.cfg, session.account
	loadingCtx, loading := check.Child(ctx, hostOf(account.URL), progress.Loading.Title("your organizations"))
	org, err := pickOrganization(loadingCtx, account, cfg.Dir)
	loading.End(err)
	if err != nil {
		return nil, err
	}
	session.org = org
	identity := consoleIdentity(org, remoteVendor(cfg), email, account.URL)
	check.Identity(&streamv1.IdentityEvent{Project: cfg.Slug, Tier: session.run.Tiers[0], Origin: &streamv1.Party{
		Vendor:    identity.GetProvider(),
		Account:   identity.GetAccount(),
		Principal: identity.GetPrincipal(),
		Location:  identity.GetLocation(),
	}})
	if t.Derived != "" {
		check.Say(t.Derived)
	}
	checkCtx := run.ContextWithSpan(ctx, check)
	if session.link, err = linkProject(checkCtx, check, account, cfg, org); err != nil {
		return nil, err
	}
	scopes, err := requiredScopes(remoteRunProcedures...)
	if err != nil {
		return nil, err
	}
	supplying, err := requiredScopes(runnerv1connect.RunnerServiceSupplyVariablesProcedure)
	if err != nil {
		return nil, err
	}
	jobs := make([]*remoteJob, 0, len(session.run.Tiers))
	for _, tier := range session.run.Tiers {
		runner, err := resolveRunner(checkCtx, check, session, tier)
		if err != nil {
			return nil, err
		}
		variableTokens := console.NewEndpointTokens(account.Client, account.Session, org.ID, runner.id, supplying...)
		jobs = append(jobs, &remoteJob{
			tier:           tier,
			runner:         runner,
			client:         runnerclient.New(runner.name, runner.url, console.NewEndpointTokens(account.Client, account.Session, org.ID, runner.id, scopes...)),
			supplying:      runnerclient.New(runner.name, runner.url, variableTokens),
			variableTokens: variableTokens,
		})
	}
	return jobs, nil
}

func requiredScopes(procedures ...string) ([]scopev1.Scope, error) {
	scopes := make([]scopev1.Scope, 0, len(procedures))
	for _, procedure := range procedures {
		required, err := scope.RequiredScope(procedure)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, required)
	}
	slices.Sort(scopes)
	return slices.Compact(scopes), nil
}

func (i Invocation) dispatch(ctx context.Context, session *remoteSession, job *remoteJob, phase *run.Span, source uploadedSource) error {
	running, cfg, r := session.running, session.cfg, session.run
	requestID := running.TraceID()
	if len(r.Tiers) > 1 {
		requestID += "-" + readiness.TierName(job.tier)
	}
	request := &consolev1.DispatchJobRequest{
		RequestId: requestID,
		ProjectId: session.link.ProjectID,
		Tier:      job.tier,
		RunnerId:  job.runner.id,
		Slug:      cfg.Slug,
		Provider:  providerID(cfg),
	}
	dispatchingCtx, dispatching := phase.Child(ctx, cfg.Slug, progress.Title{Started: "Dispatching the job to " + job.runner.name, Ended: "Dispatched the job to " + job.runner.name})
	request.Job = &runnerv1.JobRequest{
		Command:     r.Kind,
		Source:      &runnerv1.JobRequest_SourceId{SourceId: source.id},
		Git:         readGitContext(dispatchingCtx, cfg.Dir),
		Tag:         r.Tag,
		Preview:     r.Preview,
		ProjectPath: source.projectPath,
		TierValues:  r.TierValues,
		ConfigFile:  i.selectedConfigFile(cfg),
	}
	dispatched, err := i.dispatchRetrying(dispatchingCtx, session.account, request)
	if err == nil && dispatched.GetId() == "" {
		err = fmt.Errorf("%s dispatched a job and named no id for it", session.account.URL)
	}
	dispatching.End(err)
	if err != nil {
		return err
	}
	job.jobURL = JobURL(session.account.URL, cfg.Slug, dispatched.GetDeploymentId(), session.org.ID)
	printedID := terminal.EscapeControls(dispatched.GetId())
	phase.Say(fmt.Sprintf("Job %s · follow it at %s", printedID, job.jobURL))
	queued := dispatchedJob(dispatched)
	running.SetRemoteJob(job.summarize(queued))

	if r.NoWait {
		running.Succeed(fmt.Sprintf("Started job %s on %s, and it runs without this terminal: %s", printedID, job.runner.name, job.jobURL))
		return nil
	}
	_, err = i.waitForJob(run.ContextWithSpan(ctx, phase), session, job, phase, queued)
	return err
}

func (i Invocation) selectedConfigFile(cfg *project.Project) string {
	if i.ConfigPath() == "" {
		return ""
	}
	return filepath.Base(cfg.Path)
}

func (i Invocation) dispatchRetrying(ctx context.Context, account ConsoleAccount, request *consolev1.DispatchJobRequest) (*consolev1.DispatchedJob, error) {
	wait := firstDispatchWait
	for attempt := 1; ; attempt++ {
		dispatched, err := account.Client.DispatchJob(ctx, account.Session, request)
		if err == nil || attempt == dispatchAttempts || !runnerclient.IsTransient(err) {
			return dispatched, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait/2 + rand.N(wait/2)):
		}
		wait *= 2
	}
}

func (i Invocation) readRemoteLogin(t Target) (string, console.Credentials, error) {
	if t.Cloud != nil {
		return t.Cloud.ConsoleURL, t.Cloud.Credentials, nil
	}
	var credentials console.Credentials
	err := console.ErrNotLoggedIn
	if i.Console.LoadCredentials != nil {
		credentials, err = i.Console.LoadCredentials()
	}
	if err != nil || credentials.AccessToken == "" {
		return "", console.Credentials{}, &clierror.Error{Code: clierror.CodeConsoleNotLoggedIn, Hint: "ocel login",
			Cause: errors.New("--remote runs this as a job the console dispatches, and you are not logged in to a console: run `ocel login`")}
	}
	return console.BaseURL(credentials.APIURL), credentials, nil
}

func resolveRunner(ctx context.Context, check *run.Span, session *remoteSession, tier environmentv1.Tier) (remoteRunner, error) {
	cfg, account, kind := session.cfg, session.account, session.run.Kind
	ctx, resolving := check.Child(ctx, cfg.Slug, progress.Reading.Title("the runner of "+readiness.TierName(tier)))
	resolved, err := account.Client.ResolveRunner(ctx, account.Session, session.link.ProjectID, tier, providerID(cfg))
	if err != nil {
		err = fmt.Errorf("the console names no runner for %s's %s: %w", cfg.Slug, readiness.TierName(tier), err)
		resolving.End(err)
		return remoteRunner{}, err
	}
	endpoint := resolved.GetRunner()
	runner := remoteRunner{id: endpoint.GetId(), name: "runner " + endpoint.GetName(), endpoint: endpoint.GetName(), url: endpoint.GetUrl()}
	switch {
	case runner.url == "":
		err = fmt.Errorf("%s has no address the console knows yet: start it, or bind another runner to %s", runner.name, readiness.TierName(tier))
	case cfg.HasProvider() && kind != runnerv1.JobCommand_JOB_COMMAND_BUILD && !resolved.GetCredentials():
		err = &clierror.Error{Code: clierror.CodeJobUnsupported, Cause: fmt.Errorf("%s runs %s's jobs without target credentials, so its job could not reach %s: bind it to %s with credentials in the console", runner.name, readiness.TierName(tier), cfg.Provider.ID, readiness.TierName(tier))}
	}
	resolving.End(err)
	if err != nil {
		return runner, err
	}
	check.Say("Runs on " + runner.name)
	if served := endpoint.GetVersion(); served != "" && served != version.Version {
		check.Warn(fmt.Sprintf("%s runs ocel %s and this is ocel %s: the job reads the project with ocel %s", runner.name, served, version.Version, served))
	}
	return runner, err
}

func dispatchedJob(dispatched *consolev1.DispatchedJob) *runnerv1.Job {
	return &runnerv1.Job{
		Id:           dispatched.GetId(),
		ProjectId:    dispatched.GetProjectId(),
		Tier:         dispatched.GetTier(),
		Command:      dispatched.GetCommand(),
		State:        cmp.Or(dispatched.GetState(), runnerv1.JobState_JOB_STATE_QUEUED),
		Error:        dispatched.GetError(),
		DeploymentId: dispatched.GetDeploymentId(),
		CreatedAt:    dispatched.GetCreatedAt(),
		EndedAt:      dispatched.GetEndedAt(),
	}
}

func (j *remoteJob) summarize(job *runnerv1.Job) *streamv1.RemoteJob {
	return &streamv1.RemoteJob{Id: job.GetId(), Url: j.jobURL, State: job.GetState(), Result: job.GetResult(), Error: job.GetError(), Runner: j.runner.endpoint}
}

func (s *remoteSession) readQueued(ctx context.Context, jobID string) (*runnerv1.Job, error) {
	read, err := s.account.Client.GetDispatchedJob(ctx, s.account.Session, s.link.ProjectID, jobID)
	if err != nil {
		return nil, fmt.Errorf("read job %s from %s: %w", jobID, s.account.URL, err)
	}
	if runnerclient.IsJobEnded(read.GetState()) {
		return dispatchedJob(read), nil
	}
	return nil, nil
}

func (i Invocation) answerConfirmation(ctx context.Context, session *remoteSession, job *remoteJob, span *run.Span, read *runnerv1.Job) bool {
	asked := read.GetInput().GetConfirmation()
	respondent := i.Questions.Respondent
	if asked == nil || (!session.run.Yes && (respondent == nil || !respondent.CanAnswer())) {
		return false
	}
	confirmed := session.run.Yes
	if !confirmed {
		var err error
		confirmed, err = respondent.Confirm(ctx, session.running, confirmation.Question{
			Finding: terminal.EscapeControlLines(asked.GetFinding()),
			Text:    terminal.EscapeControls(asked.GetQuestion()),
		})
		if err != nil {
			return false
		}
	}
	err := job.client.AnswerConfirmation(ctx, read.GetId(), read.GetInput().GetId(), confirmed)
	switch {
	case connect.CodeOf(err) == connect.CodeFailedPrecondition:
		return true
	case err != nil:
		span.Warn(err.Error())
		return false
	case confirmed:
		span.Say("Answered yes: " + terminal.EscapeControls(asked.GetQuestion()))
	default:
		span.Say("Answered no: " + terminal.EscapeControls(asked.GetQuestion()))
	}
	return true
}

func (i Invocation) supplyVariables(ctx context.Context, session *remoteSession, job *remoteJob, span *run.Span, read *runnerv1.Job) bool {
	needed := read.GetInput().GetVariables()
	respondent := i.Questions.Respondent
	if needed == nil || i.Values == nil || respondent == nil || !respondent.CanAnswer() {
		return false
	}
	if _, err := job.variableTokens.Ensure(ctx); err != nil {
		span.Warn(fmt.Sprintf("%v: fill them in on the console instead", err))
		return false
	}
	span.Say(fmt.Sprintf("The job needs %s in %s", english.Count(len(needed.GetVariables()), "variable"), readiness.TierName(needed.GetTier())))
	values := make([]*runnerv1.VariableValue, 0, len(needed.GetVariables()))
	err := session.running.Ask(func() error {
		for _, variable := range needed.GetVariables() {
			ask := i.Values.Input
			if variable.GetSecret() {
				ask = i.Values.Secret
			}
			title := terminal.EscapeControls(variable.GetKey())
			if variable.GetFolder() != "" {
				title = terminal.EscapeControls(variable.GetFolder() + "/" + variable.GetKey())
			}
			typed, answered, err := ask(ctx, title, terminal.EscapeControls(cmp.Or(variable.GetDescription(), variable.GetReason())))
			if err != nil {
				return err
			}
			if !answered || typed == "" {
				return fmt.Errorf("%s was left empty", title)
			}
			values = append(values, &runnerv1.VariableValue{Key: variable.GetKey(), Folder: variable.GetFolder(), Value: typed})
		}
		return nil
	})
	if err != nil {
		span.Warn(fmt.Sprintf("%v: fill the values in on the console instead", err))
		return false
	}
	if err := job.supplying.SupplyVariables(ctx, read.GetId(), read.GetInput().GetId(), values); err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			return true
		}
		span.Warn(fmt.Sprintf("%v: fill them in on the console instead", err))
		return false
	}
	span.Say("Supplied " + english.Count(len(values), "value"))
	return true
}

func (i Invocation) refuseUnanswerable(ctx context.Context, session *remoteSession, job *remoteJob, read *runnerv1.Job, asked *runnerv1.Confirmation) error {
	refused := fmt.Errorf("`%s` ran as job %s, which asks %q, and no terminal is here to answer it, so the job was canceled; to run it unattended, pass --yes",
		session.run.Command, terminal.EscapeControls(read.GetId()), terminal.EscapeControls(asked.GetQuestion()))
	if err := job.client.CancelJob(ctx, read.GetId()); err != nil {
		refused = fmt.Errorf("%w (%w)", refused, err)
	}
	return clierror.NewConfirmationRequired(refused, "--yes")
}

func (i Invocation) waitForJob(ctx context.Context, session *remoteSession, job *remoteJob, phase *run.Span, queued *runnerv1.Job) (*runnerv1.Job, error) {
	running, jobURL, runnerName := session.running, job.jobURL, job.runner.name
	watchingCtx, watching := phase.Child(ctx, session.cfg.Slug, progress.Title{Started: "Waiting for the job on " + runnerName, Ended: "Waited for the job on " + runnerName})
	printedID := terminal.EscapeControls(queued.GetId())
	answered := "answered on the console"
	var opened string
	resume := func(string) {}
	sayQueued := sync.OnceFunc(func() { watching.Say("Waiting for " + runnerName + " to take the job") })
	tailCtx, stopTail := context.WithCancel(watchingCtx)
	defer stopTail()
	var unanswerable error
	ended, err := job.client.TailJob(tailCtx, queued.GetId(), runnerclient.Tail{
		Unknown: func(ctx context.Context) (*runnerv1.Job, error) {
			sayQueued()
			return session.readQueued(ctx, queued.GetId())
		},
		Logged: func(event *runnerv1.JobEvent) {
			message := terminal.EscapeControlLines(event.GetMessage())
			if event.GetLevel() >= runnerv1.JobEventLevel_JOB_EVENT_LEVEL_WARN {
				watching.Warn(message)
				return
			}
			watching.Say(message)
		},
		Changed: func(read *runnerv1.Job) {
			running.SetRemoteJob(job.summarize(read))
			switch state := read.GetState(); {
			case state == runnerv1.JobState_JOB_STATE_SUCCEEDED:
				resume(answered)
				return
			case runnerclient.IsJobEnded(state):
				resume("the job stopped waiting")
				return
			case state != runnerv1.JobState_JOB_STATE_INPUT_REQUIRED:
				resume(answered)
				watching.Say(describeJob(read))
				return
			}
			resume(answered)
			if i.answerConfirmation(watchingCtx, session, job, watching, read) || i.supplyVariables(watchingCtx, session, job, watching, read) {
				return
			}
			if asked := read.GetInput().GetConfirmation(); asked != nil {
				unanswerable = i.refuseUnanswerable(watchingCtx, session, job, read, asked)
				stopTail()
				return
			}
			input := cmp.Or(read.GetInput(), &runnerv1.JobInput{})
			resume = watching.Hold(&streamv1.WaitingEvent{Url: jobURL, Missing: missingVariables(input.GetVariables()), Input: input})
			if opened == input.GetId() && opened != "" {
				return
			}
			opened = input.GetId()
			if session.browser != nil {
				_ = session.browser(ctx, jobURL)
			}
		},
	})
	resume("this run stopped watching the job")
	if unanswerable != nil {
		watching.End(unanswerable)
		return nil, unanswerable
	}
	if ended == nil {
		ended = queued
	}
	running.SetRemoteJob(job.summarize(ended))
	switch {
	case err != nil && ctx.Err() != nil:
		watching.Say("Stopped watching; the job keeps running on " + runnerName)
		watching.End(nil)
		running.Succeed(fmt.Sprintf("Stopped watching job %s, which keeps running on %s: %s", printedID, runnerName, jobURL))
		return nil, nil
	case err != nil:
		watching.End(err)
		return nil, fmt.Errorf("%w — follow job %s at %s", err, printedID, jobURL)
	}

	switch ended.GetState() {
	case runnerv1.JobState_JOB_STATE_SUCCEEDED:
		for _, line := range jobResultLines(ended) {
			watching.Say(line)
		}
		watching.End(nil)
		running.Succeed(jobHeadline(ended, session.run.Kind, session.cfg.Slug, runnerName, jobURL))
		return ended, nil
	case runnerv1.JobState_JOB_STATE_CANCELED:
		err = &clierror.Error{
			Code:  clierror.CodeJobCanceled,
			Cause: fmt.Errorf("job %s was canceled on %s before it finished: %s", printedID, runnerName, jobURL),
		}
	default:
		failure := ended.GetError()
		var docs string
		if slices.Contains(clierror.CloudCodes(), failure.GetCode()) || slices.Contains(runner.Codes(), failure.GetCode()) {
			docs = failure.GetCode()
		}
		err = &clierror.Error{
			Code:     clierror.CodeJobFailed,
			DocsCode: docs,
			Cause:    fmt.Errorf("job %s failed on %s with %s: %s — its log is at %s", printedID, runnerName, terminal.EscapeControls(cmp.Or(failure.GetCode(), "no error code")), terminal.EscapeControlLines(cmp.Or(failure.GetMessage(), "no message")), jobURL),
		}
	}
	watching.End(err)
	return ended, err
}

func JobURL(consoleURL, slug, deploymentID, organizationID string) string {
	page := strings.TrimRight(consoleURL, "/") + "/projects/" + url.PathEscape(slug)
	if deploymentID != "" {
		page += "/deployments/" + url.PathEscape(deploymentID)
	}
	return page + "?" + url.Values{"org": {organizationID}}.Encode()
}

func jobPhase(kind runnerv1.JobCommand) progressv1.Phase {
	switch kind {
	case runnerv1.JobCommand_JOB_COMMAND_BUILD:
		return progressv1.Phase_PHASE_BUILD
	case runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM, runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY:
		return progressv1.Phase_PHASE_DESTROY
	}
	return progressv1.Phase_PHASE_DEPLOY
}

func jobHeadline(ended *runnerv1.Job, kind runnerv1.JobCommand, slug, runnerName, jobURL string) string {
	switch kind {
	case runnerv1.JobCommand_JOB_COMMAND_BUILD:
		return fmt.Sprintf("Built %s on %s: %s", slug, runnerName, jobURL)
	case runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM:
		return cmp.Or(terminal.EscapeControls(ended.GetStep()), "Tore down the preview of "+slug) + " on " + runnerName + ": " + jobURL
	case runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY:
		return cmp.Or(terminal.EscapeControls(ended.GetStep()), fmt.Sprintf("Destroyed %s of %s", readiness.TierName(ended.GetTier()), slug)) + " on " + runnerName + ": " + jobURL
	}
	if !deployedAnything(ended) {
		return cmp.Or(terminal.EscapeControls(ended.GetStep()), "Deployed nothing of "+slug+" on "+runnerName) + " · job " + jobURL
	}
	if kind == runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP {
		return fmt.Sprintf("Deployed a preview of %s on %s: %s", slug, runnerName, jobURL)
	}
	return fmt.Sprintf("Deployed %s on %s: %s", slug, runnerName, jobURL)
}

func deployedAnything(job *runnerv1.Job) bool {
	return len(job.GetResult().GetUrls()) > 0 || job.GetDeploymentId() != ""
}

func describeJob(job *runnerv1.Job) string {
	switch {
	case job.GetStep() != "":
		return terminal.EscapeControls(job.GetStep())
	case job.GetState() == runnerv1.JobState_JOB_STATE_QUEUED:
		return "Waiting for the runner"
	case job.GetPhase() != runnerv1.JobPhase_JOB_PHASE_UNSPECIFIED:
		return "Running: " + strings.ToLower(strings.TrimPrefix(job.GetPhase().String(), "JOB_PHASE_"))
	}
	return "Running"
}

func missingVariables(needed *runnerv1.VariablesNeeded) *streamv1.MissingVariables {
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

func jobResultLines(job *runnerv1.Job) []string {
	result := job.GetResult()
	lines := slices.Clone(result.GetUrls())
	for _, built := range result.GetBuilds() {
		lines = append(lines, fmt.Sprintf("Built %s as %s", built.GetApp(), built.GetBuildId()))
	}
	if id := job.GetDeploymentId(); id != "" {
		lines = append(lines, "Deployment "+id)
	}
	for i, line := range lines {
		lines[i] = terminal.EscapeControls(line)
	}
	return lines
}

func readGitContext(ctx context.Context, dir string) *runnerv1.GitContext {
	source := deployreport.ReadSource(ctx, dir, os.Getenv)
	if source == nil {
		return nil
	}
	git := &runnerv1.GitContext{Commit: source.GetCommit(), Branch: source.GetBranch(), Dirty: source.GetDirty()}
	if pr, err := strconv.ParseUint(previewid.DiscoverPRNumber(), 10, 32); err == nil {
		git.PullRequest = uint32(pr)
	}
	return git
}

func uploadSource(ctx context.Context, phase *run.Span, cfg *project.Project, runnerName string, uploader *runnerclient.Client) (sourceID, projectPath string, err error) {
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
	found, err := uploader.ListMissing(readingCtx, archive)
	reading.End(err)
	if err != nil {
		return "", "", err
	}

	if len(found.Uploads) == 0 {
		phase.Say(runnerName + " already holds every file")
	} else {
		object := fmt.Sprintf("%s (%s) to %s", english.Count(len(found.Uploads), "file"), english.Bytes(found.TotalBytes), runnerName)
		uploadingCtx, uploading := phase.Child(ctx, cfg.Slug, progress.Title{Started: "Uploading " + object, Ended: "Uploaded " + object})
		err = uploader.Send(uploadingCtx, archive, found, reportUpload(uploading))
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
	committingCtx, committing := phase.Child(ctx, cfg.Slug, progress.Title{Started: "Committing the source to " + runnerName, Ended: "Committed the source to " + runnerName})
	sourceID, err = uploader.Commit(committingCtx, archive.Manifest)
	committing.End(err)
	return sourceID, filepath.ToSlash(projectPath), err
}

func reportUpload(span *run.Span) func(runnerclient.Progress) {
	var mu sync.Mutex
	last := time.Now()
	return func(p runnerclient.Progress) {
		mu.Lock()
		defer mu.Unlock()
		if p.SentBytes == 0 || p.SentFiles == p.TotalFiles || time.Since(last) < uploadProgressEach {
			return
		}
		last = time.Now()
		span.Say(fmt.Sprintf("%s of %s · %d of %d files", english.Bytes(p.SentBytes), english.Bytes(p.TotalBytes), p.SentFiles, p.TotalFiles))
	}
}

func remoteVendor(cfg *project.Project) string {
	if cfg.HasProvider() {
		return cfg.Provider.ID
	}
	return cloudVendor
}

func consoleIdentity(org *console.Organization, vendor, email, consoleURL string) *contractv1.Identity {
	return &contractv1.Identity{Provider: vendor, Account: org.Name, Principal: email, Location: hostOf(consoleURL)}
}

func hostOf(consoleURL string) string {
	if u, err := url.Parse(consoleURL); err == nil && u.Host != "" {
		return u.Host
	}
	return consoleURL
}

const (
	noWaitFlag = "no-wait"
	remoteFlag = "remote"
)

func AddNoWaitFlag(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVar(into, noWaitFlag, false, "On a runner, start the job, print its link and return without waiting for it to end")
}

func AddRemoteFlag(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVar(into, remoteFlag, false, "Run this as a job on the runner the console binds to the project's tier: upload the source, start the job and follow it")
}

func RefuseNoWait() error {
	return &clierror.Error{Code: clierror.CodeUsage, Cause: fmt.Errorf("--%s leaves a job running on a runner, and this runs here, with no job to leave running: add --%s to run it as a job", noWaitFlag, remoteFlag)}
}
