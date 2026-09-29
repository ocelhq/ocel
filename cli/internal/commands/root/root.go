package root

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/doctor"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/commands/bindings"
	"github.com/ocelhq/ocel/cli/internal/commands/bootstrap"
	buildcommand "github.com/ocelhq/ocel/cli/internal/commands/build"
	"github.com/ocelhq/ocel/cli/internal/commands/connector"
	"github.com/ocelhq/ocel/cli/internal/commands/cost"
	"github.com/ocelhq/ocel/cli/internal/commands/deploy"
	"github.com/ocelhq/ocel/cli/internal/commands/destroy"
	"github.com/ocelhq/ocel/cli/internal/commands/dev"
	"github.com/ocelhq/ocel/cli/internal/commands/domain"
	"github.com/ocelhq/ocel/cli/internal/commands/env"
	"github.com/ocelhq/ocel/cli/internal/commands/generate"
	"github.com/ocelhq/ocel/cli/internal/commands/link"
	"github.com/ocelhq/ocel/cli/internal/commands/lock"
	"github.com/ocelhq/ocel/cli/internal/commands/login"
	"github.com/ocelhq/ocel/cli/internal/commands/permissions"
	"github.com/ocelhq/ocel/cli/internal/commands/projectinit"
	"github.com/ocelhq/ocel/cli/internal/commands/promotions"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/projecteditor"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
)

var verboseFlag bool

var configFlag string

func explicitConfigPath() string {
	if configFlag != "" {
		return configFlag
	}
	return os.Getenv(commands.ConfigEnvVar)
}

func verboseEnabled() bool {
	if verboseFlag {
		return true
	}
	_, ok := os.LookupEnv(commands.DebugEnvVar)
	return ok
}

var logFormatFlag string

var bus = run.NewBus(time.Now)

var rootCmd = &cobra.Command{
	Use:           "ocel <command>",
	Short:         "Ocel CLI",
	Long:          "Ocel CLI\n\nocel deploys apps to your own infrastructure",
	Version:       version.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var stopInterruptHandler context.CancelFunc = func() {}

func Execute() error {
	err := rootCmd.Execute()
	stopInterruptHandler()
	return errors.Join(err, bus.Close())
}

func handleInterrupts(cmd *cobra.Command) {
	install := installInterruptHandler
	if cmd == devCmd || cmd == runCmd {
		install = installDevInterruptHandler
	}
	ctx, stop := install(cmd.Root().Context(), cmd.ErrOrStderr())
	cmd.SetContext(ctx)
	stopInterruptHandler = stop
}

var devCmd, runCmd *cobra.Command

func init() {
	invocation := newInvocation()
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		invocation.AttachCommandSink(cmd)
		handleInterrupts(cmd)
	}

	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "Stream full logs instead of the progress view (also $OCEL_DEBUG)")
	rootCmd.PersistentFlags().StringVarP(&configFlag, "config", "c", "", "Project config `file` (default: $OCEL_CONFIG, else the nearest ocel.json, ocel.yaml, ocel.yml or ocel.config.ts)")
	rootCmd.PersistentFlags().StringVar(&logFormatFlag, "log-format", string(terminal.FormatHuman), "Log output format: human or json")

	devDependencies := dev.Dependencies{Invocation: invocation, OpenDocker: docker.Open}
	devCmd, runCmd = dev.NewCommand(devDependencies), dev.NewRunCommand(devDependencies)
	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(projectinit.NewCommand(projectinit.Dependencies{Invocation: invocation, RunPackageManager: projectinit.RunPackageManager}))
	rootCmd.AddCommand(generate.NewCommand(generate.Dependencies{Invocation: invocation, CollectDeclarations: declaration.Collect}))
	rootCmd.AddCommand(buildcommand.NewCommand(buildcommand.Dependencies{Invocation: invocation, BuildApps: build.Apps}))
	rootCmd.AddCommand(lock.NewCommand(invocation))
	deployDependencies := deploy.Dependencies{
		Invocation:              invocation,
		BuildApps:               build.Apps,
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		DeploymentID:            build.DeploymentID,
		CollectDeclarations:     declaration.Collect,
		OpenBrowser:             browser.OpenURL,
		ServeVariableEditor:     projecteditor.Serve,
		ReadGitBranch:           deploy.ReadGitBranch,
		DiscoverPRNumber:        deploy.DiscoverPRNumber,
	}
	rootCmd.AddCommand(deploy.NewCommand(deployDependencies))
	rootCmd.AddCommand(deploy.NewPreviewCommand(deployDependencies))
	rootCmd.AddCommand(env.NewCommand(env.Dependencies{Invocation: invocation, OpenBrowser: browser.OpenURL, ServeVariableEditor: projecteditor.Serve}))
	rootCmd.AddCommand(promotions.NewRollbackCommand(invocation))
	rootCmd.AddCommand(promotions.NewDeploymentsCommand(invocation))
	rootCmd.AddCommand(domain.NewCommand(invocation))
	rootCmd.AddCommand(bindings.NewCommand(invocation))
	rootCmd.AddCommand(destroy.NewCommand(invocation))

	rootCmd.AddCommand(bootstrap.NewCommand(invocation))
	rootCmd.AddCommand(permissions.NewCommand(invocation))
	rootCmd.AddCommand(cost.NewCommand(cost.Dependencies{Invocation: invocation, ReadFunctions: build.ReadFunctions, CollectDeclarations: declaration.Collect}))
	rootCmd.AddCommand(doctor.NewCommand(invocation))

	rootCmd.AddGroup(
		&cobra.Group{ID: coreGroup, Title: "CORE COMMANDS"},
		&cobra.Group{ID: consoleGroup, Title: "CONSOLE COMMANDS"},
	)
	loginDependencies := login.Dependencies{
		Invocation:        invocation,
		LoadCredentials:   console.LoadCredentials,
		SaveCredentials:   console.SaveCredentials,
		DeleteCredentials: console.DeleteCredentials,
		OpenBrowser:       browser.OpenURL,
	}
	linkDependencies := link.Dependencies{Invocation: invocation, LoadCredentials: console.LoadCredentials}
	loginCmd, logoutCmd, linkCmd := login.NewCommand(loginDependencies), login.NewLogoutCommand(loginDependencies), link.NewCommand(linkDependencies)
	connectorCmd := connector.NewCommand(connector.Dependencies{Invocation: invocation, LoadCredentials: console.LoadCredentials})
	readsConsoleURL(rootCmd, loginCmd, logoutCmd, linkCmd, connectorCmd)
	addConsoleCommands(rootCmd, loginCmd, logoutCmd, linkCmd, link.NewUnlinkCommand(linkDependencies), connectorCmd)

	installHelpStyle(rootCmd)
}

func newInvocation() commands.Invocation {
	return commands.Invocation{
		Events:          bus,
		Presentation:    presentation,
		StdinIsTerminal: func(in io.Reader) bool { return terminal.IsTerminal(in) },
		Questions:       providerclient.Questions{Prompt: terminal.NewPrompt(os.Stderr, os.Stdin), Out: os.Stderr},
		ConfigPath:      explicitConfigPath,
	}
}

func presentation(w io.Writer) terminal.Presentation {
	return terminal.Detect(terminal.Format(logFormatFlag), verboseEnabled(), w)
}
