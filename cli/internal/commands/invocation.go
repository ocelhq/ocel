package commands

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/confirmation"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/consolecredential"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

const NoBrowserEnvVar = "OCEL_NO_BROWSER"

const ConfigEnvVar = "OCEL_CONFIG"

const DebugEnvVar = "OCEL_DEBUG"

const JSONEnvVar = "OCEL_JSON"

type ValuePrompt interface {
	Input(ctx context.Context, title, description string) (string, bool, error)
	Secret(ctx context.Context, title, description string) (string, bool, error)
}

type Invocation struct {
	Events          *run.Bus
	Presentation    func(w io.Writer) terminal.Presentation
	StdinIsTerminal func(r io.Reader) bool
	IsJSON          func() bool
	Questions       providerprocess.Questions
	Values          ValuePrompt
	OpenBrowser     func(ctx context.Context, url string) error
	ConfigPath      func() string
	Setups          prerequisite.Setups
	RecordEvent     func(telemetry.Payload) bool

	Console           deployreport.Console
	ConsoleCredential consolecredential.Console
	CloudOffer        func(ctx context.Context, creds console.Credentials) (*console.CloudOffer, error)
	CloudSessions     *CloudSessions
	JobContext        *jobcontext.Context
}

func (i Invocation) LoadProject(ctx context.Context, cwd string) (*project.Project, error) {
	if job := i.Job(); job != nil {
		return loadJobProject(ctx, job, cwd, i.ConfigPath())
	}
	return project.Load(ctx, cwd, i.ConfigPath())
}

func (i Invocation) Job() *jobcontext.Job {
	if i.JobContext == nil {
		return nil
	}
	return i.JobContext.Job
}

func (i Invocation) Respondent(out io.Writer, in io.Reader) confirmation.Respondent {
	if job := i.Job(); job != nil && job.Input != nil {
		return confirmation.Job{Answers: job.Input}
	}
	if i.StdinIsTerminal(in) {
		terminal := confirmation.OnTerminal(out, in)
		terminal.IsJSON = i.IsJSON
		return terminal
	}
	return confirmation.Nobody{}
}

func (i Invocation) Policy(command string, yes bool, out io.Writer, in io.Reader) consent.Policy {
	policy := consent.NewPolicy(command, yes, i.CanAsk(in), out, in)
	policy.Respondent = i.Respondent(out, in)
	return policy
}

func (i Invocation) PlanPolicy(command string, yes bool, out io.Writer, in io.Reader) consent.Policy {
	policy := i.Policy(command, yes, out, in)
	policy.ConfirmsPlan = true
	return policy
}

func (i Invocation) EnsureTarget(ctx context.Context, cwd string, policy consent.Policy) (Target, error) {
	var target Target
	preamble := i.Events.Preamble(ctx)
	err := i.Setups.Ensure(ctx, policy, preamble, func(ctx context.Context) (err error) {
		target, err = i.ResolveTarget(ctx, cwd)
		return err
	})
	if prerequisite.IsDeclined(err) {
		preamble.Say(err.Error())
	}
	return target, err
}

func (i Invocation) LoadOptionalProject(ctx context.Context, cwd string) (*project.Project, error) {
	return project.LoadOptional(ctx, cwd, i.ConfigPath())
}

func (i Invocation) CanAsk(stdin io.Reader) bool {
	return i.StdinIsTerminal(stdin) && (i.IsJSON == nil || !i.IsJSON())
}

func (i Invocation) IsBrowserReachable(stdin io.Reader) bool {
	return os.Getenv(NoBrowserEnvVar) == "" && i.CanAsk(stdin)
}

func (i Invocation) AttachCommandSink(cmd *cobra.Command) {
	w := ChooseRunOutput(cmd)
	present := i.Presentation(w)
	present.SharedTerminal = isTerminalSharedWithChild(cmd)
	sink := terminal.NewSink(present, w)
	if live, ok := sink.(*terminal.LiveTranscript); ok && isStdoutReserved(cmd) {
		if stdout, ok := cmd.OutOrStdout().(terminal.File); ok && terminal.IsTerminal(stdout) {
			cmd.SetOut(live.Above(stdout))
		}
	}
	i.Events.Attach(sink)
}
