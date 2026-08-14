package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/browser"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerlocator"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/provision"
	"github.com/ocelhq/ocel/cli/internal/varsui"
)

type deps struct {
	loadCredentials      func() (credentials.Credentials, error)
	fetchProjectConfig   func(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error)
	locateProviderBinary func(ctx context.Context, projectDir, providerPackage string) (string, error)
	buildApp             func(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, out io.Writer) error
	collectAppFunctions  func(projectDir string) ([]manifestbuilder.Function, error)
	deploymentID         func(projectDir string) (string, error)
	collectDeclarations  func(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, stdout, stderr io.Writer) ([]declare.Resource, error)
	openBrowser          func(url string) error
	serveVarsUI          func(ctx context.Context, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, runner *providerrunner.Runner, preview bool, gate *envgate.Gate) (*varsui.Session, error)
	currentGitBranch     func(dir string) (string, error)
	discoverPRNumber     func() string
	runPackageManager    func(ctx context.Context, dir string, argv []string, output io.Writer) error
	stdinIsTerminal      func(r io.Reader) bool
	stdoutIsTerminal     func(w io.Writer) bool
}

func defaultDeps() deps {
	return deps{
		loadCredentials:      credentials.Load,
		fetchProjectConfig:   provision.FetchProjectConfig,
		locateProviderBinary: providerlocator.Locate,
		buildApp:             appbuilder.Build,
		collectAppFunctions:  appbuilder.CollectFunctions,
		deploymentID:         appbuilder.DeploymentID,
		collectDeclarations:  deploycollector.Collect,
		openBrowser:          browser.OpenURL,
		serveVarsUI:          startVarsUI,
		currentGitBranch:     gitBranch,
		discoverPRNumber:     prNumberFromEnv,
		runPackageManager:    runPackageManagerCommand,
		stdinIsTerminal:      isReaderTTY,
		stdoutIsTerminal:     deployui.IsTerminal,
	}
}

func gitBranch(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("determine current git branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", errors.New("determine current git branch: empty ref")
	}
	return branch, nil
}

func prNumberFromEnv() string {
	return os.Getenv("OCEL_PR_NUMBER")
}

func runPackageManagerCommand(ctx context.Context, dir string, argv []string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}
