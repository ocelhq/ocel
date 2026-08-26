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
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/cli/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/deploy"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/resolve"
)

var version = "dev"

var verboseFlag bool

var configFlag string

func explicitConfigPath() string {
	if configFlag != "" {
		return configFlag
	}
	return os.Getenv("OCEL_CONFIG")
}

func verboseEnabled() bool {
	if verboseFlag {
		return true
	}
	_, ok := os.LookupEnv("OCEL_DEBUG")
	return ok
}

const (
	logFormatHuman = "human"
	logFormatJSON  = "json"
)

var logFormatFlag string

func logFormat() string {
	if logFormatFlag == logFormatJSON {
		return logFormatJSON
	}
	return logFormatHuman
}

var rootCmd = &cobra.Command{
	Use:           "ocel <command>",
	Short:         "Ocel CLI",
	Long:          "Ocel CLI\n\nocel deploys apps to your own infrastructure",
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	s := newDeps()

	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Stream full logs instead of the progress view (also $OCEL_DEBUG)")
	rootCmd.PersistentFlags().StringVarP(&configFlag, "config", "c", "", "Project config `file` (default: $OCEL_CONFIG, else nearest ocel.config.ts)")
	rootCmd.PersistentFlags().StringVar(&logFormatFlag, "log-format", logFormatHuman, "Log output format: human or json")

	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(generateCmd)
	rootCmd.AddCommand(buildCmd)
	rootCmd.AddCommand(deploy.NewCommand(s))
	rootCmd.AddCommand(deploy.NewPreviewCommand(s))
	rootCmd.AddCommand(rollbackCmd)
	rootCmd.AddCommand(deploymentsCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)

	rootCmd.AddCommand(bootstrap.NewCommand(s))
	rootCmd.AddCommand(newPermissionsCommand(s))

	installHelpStyle(rootCmd)
}

func newDeps() cmddeps.Deps {
	return cmddeps.Deps{
		LoadCredentials:     credentials.Load,
		FetchAccount:        resolve.StubAccount,
		BuildApp:            appbuilder.Build,
		CollectAppFunctions: appbuilder.CollectFunctions,
		DeploymentID:        appbuilder.DeploymentID,
		CollectDeclarations: deploycollector.Collect,
		OpenBrowser:         browser.OpenURL,
		ServeVarsUI:         envwire.ServeVarsUI,
		CurrentGitBranch:    gitBranch,
		DiscoverPRNumber:    prNumberFromEnv,
		RunPackageManager:   runPackageManagerCommand,
		HostTrust:           provider.Trust{Ask: prompt.New(os.Stderr, os.Stdin), Out: os.Stderr},
		StdinIsTerminal:     isReaderTTY,
		StdoutIsTerminal:    deployui.IsTerminal,
		ConfigPath:          explicitConfigPath,
		Verbose:             verboseEnabled,
		Format:              sessionFormat,
		Interrupt:           installInterruptHandler,
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

func sessionFormat() deployui.Format {
	if logFormat() == logFormatJSON {
		return deployui.FormatJSON
	}
	return deployui.FormatHuman
}
