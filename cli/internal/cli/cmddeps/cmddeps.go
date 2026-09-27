package cmddeps

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/varsui"
)

type Deps struct {
	LoadCredentials     func() (credentials.Credentials, error)
	OpenDocker          docker.Opener
	BuildApp            func(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, out io.Writer) error
	RequireImageBuilder func(ctx context.Context, scope *events.Scope, cfg *projectconfig.Config, archs map[string]string) error
	BuildAppImages      func(ctx context.Context, cfg *projectconfig.Config, archs map[string]string, out io.Writer) (map[string]string, error)
	CollectAppFunctions func(projectDir string) ([]manifestbuilder.Function, error)
	DeploymentID        func(projectDir, app string) (string, error)
	CollectDeclarations func(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, stdout, stderr io.Writer) ([]declare.Resource, error)
	OpenBrowser         func(url string) error
	ProbePostgres       inlinebinding.PostgresProbe
	ProbeBucket         inlinebinding.BucketProbe
	ServeVarsUI         func(ctx context.Context, cfg *projectconfig.Config, prov *providerclient.Provider, preview bool, gate *envgate.Gate, recovery *varsui.Recovery) (*varsui.Session, error)
	CurrentGitBranch    func(dir string) (string, error)
	DiscoverPRNumber    func() string
	RunPackageManager   func(ctx context.Context, dir string, argv []string, output io.Writer) error
	HostTrust           providerclient.Trust
	StdinIsTerminal     func(r io.Reader) bool
	ConfigPath          func() string
	Presentation        func(w io.Writer) runui.Presentation
	Events              *events.Bus
	Interrupt           func(ctx context.Context, stderr io.Writer) (context.Context, context.CancelFunc)
}

const NoBrowserEnvVar = "OCEL_NO_BROWSER"

func (d Deps) BrowserReachable(stdin io.Reader) bool {
	return os.Getenv(NoBrowserEnvVar) == "" && d.StdinIsTerminal(stdin)
}

func (d Deps) AttachTerminalSink(w io.Writer) {
	d.Events.Attach(runui.NewTerminalSink(d.Presentation(w), w))
}

func (d Deps) AttachCommandSink(cmd *cobra.Command) {
	w := ChooseRunOutput(cmd)
	present := d.Presentation(w)
	present.SharedTerminal = isStdoutReserved(cmd) && runui.IsTerminal(cmd.OutOrStdout())
	d.Events.Attach(runui.NewTerminalSink(present, w))
}

const stdoutAnnotation = "ocel.stdout"

func ReserveStdout(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[stdoutAnnotation] = "data"
	return cmd
}

func ChooseRunOutput(cmd *cobra.Command) io.Writer {
	if isStdoutReserved(cmd) {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

func isStdoutReserved(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[stdoutAnnotation] == "data" {
			return true
		}
	}
	return false
}

const YesUsage = "Consent in advance to any confirmation this command would ask for"

func Yes(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVarP(into, "yes", "y", false, YesUsage)
}

func (d Deps) Gate(class consent.Class, command string, yes bool, stdout io.Writer, stdin io.Reader) consent.Gate {
	return consent.Gate{
		Command:     command,
		Class:       class,
		Yes:         yes,
		Interactive: d.StdinIsTerminal(stdin),
		In:          stdin,
		Out:         stdout,
	}
}
