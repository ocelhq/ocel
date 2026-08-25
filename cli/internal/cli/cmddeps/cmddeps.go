package cmddeps

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/resolve"
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
	StdinIsTerminal     func(r io.Reader) bool
	StdoutIsTerminal    func(w io.Writer) bool
	ConfigPath          func() string
	Verbose             func() bool
	Format              func() deployui.Format
	Interrupt           func(ctx context.Context, stderr io.Writer) (context.Context, context.CancelFunc)
}
