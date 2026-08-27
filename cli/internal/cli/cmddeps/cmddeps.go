package cmddeps

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/varsui"
)

type Deps struct {
	LoadCredentials     func() (credentials.Credentials, error)
	FetchAccount        func(ctx context.Context, apiURL, token, projectID string) (resolve.Account, error)
	BuildApp            func(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, out io.Writer) error
	CollectAppFunctions func(projectDir string) ([]manifestbuilder.Function, error)
	DeploymentID        func(projectDir, app string) (string, error)
	CollectDeclarations func(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, stdout, stderr io.Writer) ([]declare.Resource, error)
	OpenBrowser         func(url string) error
	ServeVarsUI         func(ctx context.Context, cfg *projectconfig.Config, runner *provider.Runner, preview bool, gate *envgate.Gate) (*varsui.Session, error)
	CurrentGitBranch    func(dir string) (string, error)
	DiscoverPRNumber    func() string
	RunPackageManager   func(ctx context.Context, dir string, argv []string, output io.Writer) error
	HostTrust           provider.Trust
	StdinIsTerminal     func(r io.Reader) bool
	ConfigPath          func() string
	Presentation        func(w io.Writer) runui.Presentation
	Interrupt           func(ctx context.Context, stderr io.Writer) (context.Context, context.CancelFunc)
}

const YesUsage = "Consent in advance to any confirmation this command would ask for"

func Yes(cmd *cobra.Command, into *bool) {
	cmd.Flags().BoolVarP(into, "yes", "y", false, YesUsage)
}

func (d Deps) Spec(consent runui.Consent, command string, cfg *projectconfig.Config, yes bool, stdout io.Writer, stdin io.Reader) runui.Spec {
	return runui.Spec{
		Command:     command,
		Consent:     consent,
		Yes:         yes,
		Config:      cfg,
		Present:     d.Presentation(stdout),
		Trust:       d.HostTrust,
		Interactive: d.StdinIsTerminal(stdin),
		Stdout:      stdout,
		Stdin:       stdin,
	}
}
