package env

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type Dependencies struct {
	commands.Invocation
	OpenBrowser         func(url string) error
	ServeVariableEditor func(ctx context.Context, cfg *project.Project, provider *providerprocess.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error)
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "env <command>",
		Short:   "Manage this project's variable values",
		Example: "  $ ocel env ls\n  $ ocel env set LOG_LEVEL=debug\n  $ ocel env ui --preview",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return &clierror.Error{Code: clierror.CodeUsage, Cause: &exitcode.ExitError{Code: 1}}
		},
	}
	cmd.AddCommand(
		newListCommand(dependencies),
		newSetCommand(dependencies),
		newGetCommand(dependencies),
		newRemoveCommand(dependencies),
		newRefCommand(dependencies),
		newRefsCommand(dependencies),
		newHistoryCommand(dependencies),
		newUICommand(dependencies),
		newEnvSourceCommand(dependencies),
		newSyncCommand(dependencies),
	)
	return commands.DeclareReadOnly(commands.ReserveStdout(cmd))
}

func newListCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List values without revealing them",
		Example: "  $ ocel env ls\n  $ ocel env ls --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvList(ctx, dependencies, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.EnvListResult{})
}

func newSetCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "set <KEY=VALUE>...",
		Short:   "Set a value",
		Example: "  $ ocel env set LOG_LEVEL=debug\n  $ ocel env set LOG_LEVEL=debug FEATURE_FLAG=true --folder /web",
		Args:    cobra.MinimumNArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		pairs, err := parseEnvSetPairs(args)
		if err != nil {
			return err
		}
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvSetPairs(ctx, dependencies, cwd, pairs, opts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	return commands.DeclareMutating(cmd)
}

type envSetPair struct {
	key   string
	value string
}

func parseEnvSetPairs(args []string) ([]envSetPair, error) {
	pairs := make([]envSetPair, 0, len(args))
	for i, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		switch {
		case !ok:
			return nil, fmt.Errorf("argument %d has no =: `ocel env set` takes KEY=VALUE, and that argument is not repeated here in case it is a value", i+1)
		case key == "":
			return nil, fmt.Errorf("argument %d starts with =, so it names no key: `ocel env set` takes KEY=VALUE", i+1)
		}
		pairs = append(pairs, envSetPair{key: key, value: value})
	}
	return pairs, nil
}

func newGetCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "get <KEY>",
		Short:   "Inspect a value",
		Example: "  $ ocel env get LOG_LEVEL\n  $ ocel env get LOG_LEVEL --reveal\n  $ ocel env get STRIPE_API_KEY --reveal --yes",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvGet(ctx, dependencies, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	cmd.Flags().BoolVar(&opts.reveal, "reveal", false, "Print the value")
	commands.AddYesFlag(cmd, &opts.yes)
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.EnvGetResult{})
}

func newRemoveCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "rm <KEY>",
		Short:   "Remove a value",
		Example: "  $ ocel env rm LOG_LEVEL\n  $ ocel env rm LOG_LEVEL --preview --environment staging",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvRemove(ctx, dependencies, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	return commands.DeclareMutating(cmd)
}

func newRefCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	var ref envRefOptions
	cmd := &cobra.Command{
		Use:     "ref <KEY>",
		Short:   "Read a value stored elsewhere",
		Long:    "Read a value stored under another project, folder, or key.\n\nUpdates to the stored value reach every reference.",
		Example: "  $ ocel env ref LOG_LEVEL --target-project shared\n  $ ocel env ref LOG_LEVEL --folder /web --target-folder /api",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvRef(ctx, dependencies, cwd, args[0], opts, ref, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	cmd.Flags().StringVar(&ref.project, "target-project", "", "Read from another `project`")
	cmd.Flags().StringVar(&ref.folder, "target-folder", "", "Read from a `folder` in the target project")
	cmd.Flags().StringVar(&ref.key, "target-key", "", "Read a different target `key`")
	return commands.DeclareMutating(cmd)
}

func newRefsCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "refs <KEY>",
		Short:   "List references to a value",
		Example: "  $ ocel env refs LOG_LEVEL\n  $ ocel env refs LOG_LEVEL --folder /web",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvRefs(ctx, dependencies, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.EnvRefsResult{})
}

func newHistoryCommand(dependencies Dependencies) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "history <KEY>",
		Short:   "List a value's history",
		Example: "  $ ocel env history LOG_LEVEL\n  $ ocel env history LOG_LEVEL --preview",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, dependencies, func(ctx context.Context, cwd string) error {
			return runEnvHistory(ctx, dependencies, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	return commands.DeclareResult(commands.DeclareReadOnly(cmd), &resultv1.EnvHistoryResult{})
}

func previewFlag(cmd *cobra.Command, opts *envOptions) {
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "Use preview values")
}

func valueFlags(cmd *cobra.Command, opts *envOptions) {
	previewFlag(cmd, opts)
	cmd.Flags().StringVar(&opts.folder, "folder", "", "Use the value in this `folder`")
}

func environmentFlag(cmd *cobra.Command, opts *envOptions) {
	cmd.Flags().StringVar(&opts.environment, "environment", "", "Use this named preview `environment`; requires --preview")
}
