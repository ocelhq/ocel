package cmddeps

import (
	"context"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/inlinebinding"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type Deps struct {
	LoadCredentials         func() (console.Credentials, error)
	SaveCredentials         func(console.Credentials) (console.CredentialStore, error)
	DeleteCredentials       func() error
	OpenDocker              docker.OpenFunc
	BuildApps               func(ctx context.Context, cfg *projectconfig.Config, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error)
	RefuseUnbuildableImages func(ctx context.Context, span *run.Span, cfg *projectconfig.Config, archs map[string]string) error
	ReadPrebuilt            func(ctx context.Context, cfg *projectconfig.Config, archs map[string]string) (build.Output, error)
	ReadFunctions           func(projectDir string) ([]manifestbuilder.Function, error)
	DeploymentID            func(projectDir, app string) (string, error)
	CollectDeclarations     func(ctx context.Context, cfg *projectconfig.Config, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
	OpenBrowser             func(url string) error
	ProbePostgres           inlinebinding.PostgresProbe
	ProbeBucket             inlinebinding.BucketProbe
	ServeVariableEditor     func(ctx context.Context, cfg *projectconfig.Config, prov *providerclient.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error)
	CurrentGitBranch        func(dir string) (string, error)
	DiscoverPRNumber        func() string
	RunPackageManager       func(ctx context.Context, dir string, argv []string, output io.Writer) error
	HostTrust               providerclient.Trust
	StdinIsTerminal         func(r io.Reader) bool
	ConfigPath              func() string
	Presentation            func(w io.Writer) runui.Presentation
	Events                  *run.Bus
}

const NoBrowserEnvVar = "OCEL_NO_BROWSER"

func (d Deps) BrowserReachable(stdin io.Reader) bool {
	return os.Getenv(NoBrowserEnvVar) == "" && d.StdinIsTerminal(stdin)
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
