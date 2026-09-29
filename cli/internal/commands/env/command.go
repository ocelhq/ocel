package env

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "env <command>",
		Short:   "Manage this project's variable values",
		Example: "  $ ocel env ls\n  $ ocel env set LOG_LEVEL=debug\n  $ ocel env ui --preview",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return &exitcode.ExitError{Code: 1}
		},
	}
	cmd.AddCommand(
		newLsCommand(deps),
		newSetCommand(deps),
		newGetCommand(deps),
		newRmCommand(deps),
		newRefCommand(deps),
		newRefsCommand(deps),
		newHistoryCommand(deps),
		newUICommand(deps),
		newEnvSourceCommand(deps),
		newSyncCommand(deps),
	)
	return cmddeps.ReserveStdout(cmd)
}

func newLsCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "ls",
		Short:   "List values without revealing them",
		Example: "  $ ocel env ls\n  $ ocel env ls --preview",
		Args:    cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvLs(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	previewFlag(cmd, &opts)
	return cmd
}

func newSetCommand(deps cmddeps.Deps) *cobra.Command {
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
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvSetPairs(ctx, deps, cwd, pairs, opts, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	return cmd
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

func newGetCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "get <KEY>",
		Short:   "Inspect a value",
		Example: "  $ ocel env get LOG_LEVEL\n  $ ocel env get LOG_LEVEL --reveal\n  $ ocel env get STRIPE_API_KEY --reveal --yes",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvGet(ctx, deps, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	cmd.Flags().BoolVar(&opts.reveal, "reveal", false, "Print the value")
	cmddeps.Yes(cmd, &opts.yes)
	return cmd
}

func newRmCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "rm <KEY>",
		Short:   "Remove a value",
		Example: "  $ ocel env rm LOG_LEVEL\n  $ ocel env rm LOG_LEVEL --preview --environment staging",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvRm(ctx, deps, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	return cmd
}

func newRefCommand(deps cmddeps.Deps) *cobra.Command {
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
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvRef(ctx, deps, cwd, args[0], opts, ref, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	cmd.Flags().StringVar(&ref.project, "target-project", "", "Read from another `project`")
	cmd.Flags().StringVar(&ref.folder, "target-folder", "", "Read from a `folder` in the target project")
	cmd.Flags().StringVar(&ref.key, "target-key", "", "Read a different target `key`")
	return cmd
}

func newRefsCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "refs <KEY>",
		Short:   "List references to a value",
		Example: "  $ ocel env refs LOG_LEVEL\n  $ ocel env refs LOG_LEVEL --folder /web",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvRefs(ctx, deps, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	return cmd
}

func newHistoryCommand(deps cmddeps.Deps) *cobra.Command {
	var opts envOptions
	cmd := &cobra.Command{
		Use:     "history <KEY>",
		Short:   "List a value's history",
		Example: "  $ ocel env history LOG_LEVEL\n  $ ocel env history LOG_LEVEL --preview",
		Args:    cobra.ExactArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withCommand(cmd, deps, func(ctx context.Context, cwd string) error {
			return runEnvHistory(ctx, deps, cwd, args[0], opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	}
	valueFlags(cmd, &opts)
	environmentFlag(cmd, &opts)
	return cmd
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
