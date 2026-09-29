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
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type Deps struct {
	LoadCredentials         func() (console.Credentials, error)
	SaveCredentials         func(console.Credentials) (console.CredentialStore, error)
	DeleteCredentials       func() error
	OpenDocker              docker.OpenFunc
	BuildApps               func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error)
	RefuseUnbuildableImages func(ctx context.Context, span *run.Span, cfg *project.Project, archs map[string]string) error
	ReadPrebuilt            func(ctx context.Context, cfg *project.Project, archs map[string]string) (build.Output, error)
	ReadFunctions           func(projectDir string) ([]build.Function, error)
	DeploymentID            func(projectDir, app string) (string, error)
	CollectDeclarations     func(ctx context.Context, cfg *project.Project, declarations *variables.Declarations, stdout, stderr io.Writer) ([]declaration.Resource, error)
	OpenBrowser             func(url string) error
	ServeVariableEditor     func(ctx context.Context, cfg *project.Project, prov *providerclient.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error)
	CurrentGitBranch        func(dir string) (string, error)
	DiscoverPRNumber        func() string
	RunPackageManager       func(ctx context.Context, dir string, argv []string, output io.Writer) error
	HostTrust               providerclient.Trust
	StdinIsTerminal         func(r io.Reader) bool
	ConfigPath              func() string
	Presentation            func(w io.Writer) terminal.Presentation
	Events                  *run.Bus
}

func (d Deps) LoadProject(ctx context.Context, cwd string) (*project.Project, error) {
	return project.Load(ctx, cwd, d.ConfigPath())
}

func (d Deps) LoadOptionalProject(ctx context.Context, cwd string) (*project.Project, error) {
	return project.LoadOptional(ctx, cwd, d.ConfigPath())
}

const NoBrowserEnvVar = "OCEL_NO_BROWSER"

func (d Deps) BrowserReachable(stdin io.Reader) bool {
	return os.Getenv(NoBrowserEnvVar) == "" && d.StdinIsTerminal(stdin)
}

func (d Deps) AttachCommandSink(cmd *cobra.Command) {
	w := ChooseRunOutput(cmd)
	present := d.Presentation(w)
	present.SharedTerminal = isStdoutReserved(cmd) && terminal.IsTerminal(cmd.OutOrStdout())
	d.Events.Attach(terminal.NewSink(present, w))
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

func (d Deps) ConsentPolicy(command string, yes bool, stdout io.Writer, stdin io.Reader) consent.Policy {
	return consent.Policy{
		Command:     command,
		Yes:         yes,
		Interactive: d.StdinIsTerminal(stdin),
		In:          stdin,
		Out:         stdout,
	}
}
