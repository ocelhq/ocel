package root

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
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
	"github.com/ocelhq/ocel/cli/internal/commands/logs"
	"github.com/ocelhq/ocel/cli/internal/commands/permissions"
	"github.com/ocelhq/ocel/cli/internal/commands/projectinit"
	"github.com/ocelhq/ocel/cli/internal/commands/promotions"
	"github.com/ocelhq/ocel/cli/internal/commands/schema"
	"github.com/ocelhq/ocel/cli/internal/commands/telemetryflush"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
)

type flags struct {
	verbose bool
	config  string
	json    bool
}

func (f *flags) explicitConfigPath() string {
	if f.config != "" {
		return f.config
	}
	return os.Getenv(commands.ConfigEnvVar)
}

func (f *flags) readFromFlagsOrEnv(cmd *cobra.Command) error {
	var errVerbose, errJSON error
	f.verbose, errVerbose = readFlagOrEnvBool(cmd.Flags(), "verbose", commands.DebugEnvVar)
	f.json, errJSON = readFlagOrEnvBool(cmd.Flags(), "json", commands.JSONEnvVar)
	return errors.Join(errVerbose, errJSON)
}

func readFlagOrEnvBool(set *pflag.FlagSet, flag, envVar string) (bool, error) {
	if set.Changed(flag) {
		return set.GetBool(flag)
	}
	return envBool(envVar)
}

func envBool(name string) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return false, nil
	}
	on, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean: want 1, true, 0, false or empty", name, value)
	}
	return on, nil
}

func (f *flags) presentation(w io.Writer) terminal.Presentation {
	format := terminal.FormatHuman
	if f.json {
		format = terminal.FormatJSON
	}
	return terminal.Detect(format, f.verbose, w)
}

type command struct {
	root                 *cobra.Command
	bus                  *run.Bus
	stopInterruptHandler context.CancelFunc
	preRunReached        bool
	invoked              *cobra.Command
	startFlush           func(telemetry.Resolution)
}

func Execute() int {
	return newCommand().executeAndReport(os.Args[1:])
}

func (c *command) execute() error {
	invoked, err := c.root.ExecuteC()
	c.invoked = invoked
	if err != nil && !c.preRunReached {
		usage := invoked
		if usage.Hidden {
			usage = c.root
		}
		err = &clierror.Error{Code: clierror.CodeUsage, Hint: usage.UseLine(), Cause: err}
	}
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
		startFlush:           startTelemetryFlush,
	}
	rootCmd := c.root
	set := &flags{}
	invocation := newInvocation(c.bus, set)
	invocation.Setups = prerequisite.Setups{}
	invocation.RecordEvent = c.recordEvent

	rootCmd.PersistentFlags().BoolVarP(&set.verbose, "verbose", "v", false, "Stream full logs instead of the progress view (also $OCEL_DEBUG)")
	rootCmd.PersistentFlags().StringVarP(&set.config, "config", "c", "", "Project config `file` (default: $OCEL_CONFIG, else the nearest ocel.json, ocel.yaml, ocel.yml or ocel.config.ts)")
	rootCmd.PersistentFlags().BoolVar(&set.json, "json", false, "Write the run's events as NDJSON instead of the human view (also $OCEL_JSON)")

	devDependencies := dev.Dependencies{Invocation: invocation, OpenDocker: docker.Open}
	devCmd, runCmd := dev.NewCommand(devDependencies), dev.NewRunCommand(devDependencies)
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if err := errors.Join(cmd.ValidateRequiredFlags(), cmd.ValidateFlagGroups()); err != nil {
			return err
		}
		c.preRunReached = true
		if err := set.readFromFlagsOrEnv(cmd); err != nil {
			return err
		}
		if !isShellCompletion(cmd) {
			telemetry.PrintBannerOnce(cmd.ErrOrStderr(), telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint))
		}
		invocation.AttachCommandSink(cmd)
		install := installInterruptHandler
		if cmd == devCmd || cmd == runCmd {
			install = installDevInterruptHandler
		}
		ctx, stop := install(cmd.Root().Context(), cmd.ErrOrStderr(), c.bus)
		cmd.SetContext(ctx)
		c.stopInterruptHandler = stop
		return nil
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
	rootCmd.AddCommand(lock.NewCommand(lock.Dependencies{Invocation: invocation, Pin: executables.Pin}))
	deployDependencies := deploy.Dependencies{
		Invocation:              invocation,
		BuildApps:               build.Apps,
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		BuildID:                 build.BuildID,
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
	rootCmd.AddCommand(promotions.NewPromotionsCommand(invocation))
	rootCmd.AddCommand(logs.NewCommand(logs.Dependencies{Invocation: invocation, ReadGitBranch: deploy.ReadGitBranch, DiscoverPRNumber: deploy.DiscoverPRNumber, Now: time.Now}))
	rootCmd.AddCommand(domain.NewCommand(invocation))
	rootCmd.AddCommand(bindings.NewCommand(invocation))
	rootCmd.AddCommand(destroy.NewCommand(invocation))

	rootCmd.AddCommand(bootstrap.NewCommand(invocation))
	rootCmd.AddCommand(permissions.NewCommand(invocation))
	rootCmd.AddCommand(cost.NewCommand(cost.Dependencies{Invocation: invocation, ReadFunctions: build.ReadFunctions, CollectDeclarations: declaration.Collect}))
	rootCmd.AddCommand(doctor.NewCommand(invocation))
	rootCmd.AddCommand(schema.NewCommand(invocation))
	rootCmd.AddCommand(telemetryflush.NewCommand())

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

	installHelpStyle(rootCmd, func() bool { return set.json })
	return c
}

func startTelemetryFlush(resolution telemetry.Resolution) {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	telemetry.StartFlush(executable, resolution)
}

func isShellCompletion(cmd *cobra.Command) bool {
	return cmd.Name() == cobra.ShellCompRequestCmd || cmd.Name() == cobra.ShellCompNoDescRequestCmd
}

func newInvocation(bus *run.Bus, set *flags) commands.Invocation {
	isJSON := func() bool { return set.json }
	return commands.Invocation{
		Events:          bus,
		Presentation:    set.presentation,
		StdinIsTerminal: func(in io.Reader) bool { return terminal.IsTerminal(in) },
		IsJSON:          isJSON,
		Questions:       providerprocess.Questions{Prompt: terminal.NewPrompt(os.Stderr, os.Stdin), Out: os.Stderr, IsJSON: isJSON},
		ConfigPath:      set.explicitConfigPath,

		DeploymentReports: deployreport.Console{LoadCredentials: console.LoadCredentials},
	}
}
