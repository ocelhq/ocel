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
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/commands/bindings"
	"github.com/ocelhq/ocel/cli/internal/commands/bootstrap"
	buildcommand "github.com/ocelhq/ocel/cli/internal/commands/build"
	"github.com/ocelhq/ocel/cli/internal/commands/connector"
	"github.com/ocelhq/ocel/cli/internal/commands/cost"
	"github.com/ocelhq/ocel/cli/internal/commands/deploy"
	"github.com/ocelhq/ocel/cli/internal/commands/destroy"
	"github.com/ocelhq/ocel/cli/internal/commands/dev"
	"github.com/ocelhq/ocel/cli/internal/commands/doctor"
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
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
)

type flags struct {
	verbose   bool
	config    string
	logFormat string
}

func (f *flags) explicitConfigPath() string {
	if f.config != "" {
		return f.config
	}
	return os.Getenv(commands.ConfigEnvVar)
}

func (f *flags) isVerbose() bool {
	if f.verbose {
		return true
	}
	_, ok := os.LookupEnv(commands.DebugEnvVar)
	return ok
}

func (f *flags) presentation(w io.Writer) terminal.Presentation {
	return terminal.Detect(terminal.Format(f.logFormat), f.isVerbose(), w)
}

type command struct {
	root                 *cobra.Command
	bus                  *run.Bus
	stopInterruptHandler context.CancelFunc
}

func Execute() error {
	return newCommand().execute()
}

func (c *command) execute() error {
	err := c.root.Execute()
	c.stopInterruptHandler()
	return errors.Join(err, c.bus.Close())
}

func newCommand() *command {
	c := &command{
		root: &cobra.Command{
			Use:           "ocel <command>",
			Short:         "Ocel CLI",
			Long:          "Ocel CLI\n\nocel deploys apps to your own infrastructure",
			Version:       version.Version,
			SilenceUsage:  true,
			SilenceErrors: true,
		},
		bus:                  run.NewBus(time.Now),
		stopInterruptHandler: func() {},
	}
	rootCmd := c.root
	set := &flags{}
	invocation := newInvocation(c.bus, set)
	invocation.Setups = prerequisite.Setups{}

	rootCmd.PersistentFlags().BoolVarP(&set.verbose, "verbose", "v", false, "Stream full logs instead of the progress view (also $OCEL_DEBUG)")
	rootCmd.PersistentFlags().StringVarP(&set.config, "config", "c", "", "Project config `file` (default: $OCEL_CONFIG, else the nearest ocel.json, ocel.yaml, ocel.yml or ocel.config.ts)")
	rootCmd.PersistentFlags().StringVar(&set.logFormat, "log-format", string(terminal.FormatHuman), "Log output format: human or json")

	devDependencies := dev.Dependencies{Invocation: invocation, OpenDocker: docker.Open}
	devCmd, runCmd := dev.NewCommand(devDependencies), dev.NewRunCommand(devDependencies)
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		invocation.AttachCommandSink(cmd)
		install := installInterruptHandler
		if cmd == devCmd || cmd == runCmd {
			install = installDevInterruptHandler
		}
		ctx, stop := install(cmd.Root().Context(), cmd.ErrOrStderr(), c.bus)
		cmd.SetContext(ctx)
		c.stopInterruptHandler = stop
	}
	rootCmd.AddCommand(devCmd)
	rootCmd.AddCommand(runCmd)
	initDependencies := projectinit.Dependencies{Invocation: invocation, RunPackageManager: projectinit.RunPackageManager}
	invocation.Setups[prerequisite.Project] = projectinit.NewSetup(initDependencies)
	invocation.Setups[prerequisite.Bootstrap] = bootstrap.NewSetup()
	invocation.Setups[prerequisite.Domain] = domain.NewSetup()
	rootCmd.AddCommand(projectinit.NewCommand(initDependencies))
	rootCmd.AddCommand(generate.NewCommand(generate.Dependencies{Invocation: invocation, CollectDeclarations: declaration.Collect}))
	rootCmd.AddCommand(buildcommand.NewCommand(buildcommand.Dependencies{Invocation: invocation, BuildApps: build.Apps, CollectDeclarations: declaration.Collect}))
	rootCmd.AddCommand(lock.NewCommand(invocation))
	deployDependencies := deploy.Dependencies{
		Invocation:              invocation,
		BuildApps:               build.Apps,
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		DeploymentID:            build.DeploymentID,
		CollectDeclarations:     declaration.Collect,
		OpenBrowser:             browser.OpenURL,
		ServeVariableEditor:     env.ServeVariableEditor,
		ReadGitBranch:           deploy.ReadGitBranch,
		DiscoverPRNumber:        deploy.DiscoverPRNumber,
	}
	rootCmd.AddCommand(deploy.NewCommand(deployDependencies))
	rootCmd.AddCommand(deploy.NewPreviewCommand(deployDependencies))
	rootCmd.AddCommand(env.NewCommand(env.Dependencies{Invocation: invocation, OpenBrowser: browser.OpenURL, ServeVariableEditor: env.ServeVariableEditor}))
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
	return c
}

func newInvocation(bus *run.Bus, set *flags) commands.Invocation {
	return commands.Invocation{
		Events:          bus,
		Presentation:    set.presentation,
		StdinIsTerminal: func(in io.Reader) bool { return terminal.IsTerminal(in) },
		Questions:       providerprocess.Questions{Prompt: terminal.NewPrompt(os.Stderr, os.Stdin), Out: os.Stderr},
		ConfigPath:      set.explicitConfigPath,
	}
}
