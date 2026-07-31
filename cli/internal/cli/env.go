package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// envOptions are the flags every `ocel env` subcommand shares. folder
// addresses the cell; preview selects the substrate; reveal is the deliberate
// act that prints a value.
type envOptions struct {
	preview bool
	folder  string
	reveal  bool
}

var envOpts envOptions

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage this project's variable values",
	Long: "Manage the values behind the variables your code declares.\n\n" +
		"Which variables a project requires is declared in code; this command manages what " +
		"they are set to. Values live in your own cloud account and are reached through the " +
		"provider, never by the CLI directly.",
}

var envLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List this project's values, without revealing them",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvLs(ctx, cwd, envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envSetCmd = &cobra.Command{
	Use:   "set <KEY> <VALUE>",
	Short: "Set a value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvSet(ctx, cwd, args[0], args[1], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envGetCmd = &cobra.Command{
	Use:   "get <KEY>",
	Short: "Show one value's metadata, or the value itself with --reveal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvGet(ctx, cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envRmCmd = &cobra.Command{
	Use:   "rm <KEY>",
	Short: "Remove a value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvRm(ctx, cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envHistoryCmd = &cobra.Command{
	Use:   "history <KEY>",
	Short: "Show a value's change history, newest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvHistory(ctx, cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{envLsCmd, envSetCmd, envGetCmd, envRmCmd, envHistoryCmd} {
		c.Flags().BoolVar(&envOpts.preview, "preview", false, "Act on the preview substrate instead of production")
		envCmd.AddCommand(c)
	}
	for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd} {
		c.Flags().StringVar(&envOpts.folder, "folder", "", "Address the value in this folder (e.g. /checkout) instead of the project root")
	}
	// Reveal is `get`'s alone. History is metadata only: one keystroke that
	// printed every retained version would keep a rotated-away secret readable
	// for the whole window.
	envGetCmd.Flags().BoolVar(&envOpts.reveal, "reveal", false, "Print the value itself; without it only metadata is shown")
	rootCmd.AddCommand(envCmd)
}

// withEnvCommand resolves the working directory and a signal-aware context,
// the preamble every `ocel env` subcommand shares.
func withEnvCommand(cmd *cobra.Command, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, cwd)
}

// envSession resolves the project, spawns its provider, preflights the
// substrate the flags selected, and hands the ready runner to drive. Every
// `ocel env` subcommand goes through it: the store is only reachable through a
// live provider session.
func envSession(ctx context.Context, cwd string, opts envOptions, stdout, stderr io.Writer, drive func(*providerrunner.Runner, *projectconfig.Config, *projectconfig.ProviderDescriptor) error) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	class, hint := deploymentsv1.Environment_CLASS_PRODUCTION, "ocel bootstrap"
	if opts.preview {
		class, hint = deploymentsv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview"
	}

	return runProviderSession(ctx, cfg, provider, stderr, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightClass(ctx, runner, provider, class, hint, stderr); err != nil {
			return err
		}
		return drive(runner, cfg, provider)
	})
}

// envClass maps the substrate flag onto the class the provider opens its store
// for. Preview and production ciphertext are encrypted under different keys,
// so this is which store is opened, not a filter over one.
func envClass(opts envOptions) deploymentsv1.Environment_Class {
	if opts.preview {
		return deploymentsv1.Environment_CLASS_PREVIEW
	}
	return deploymentsv1.Environment_CLASS_PRODUCTION
}

// envCoordinate addresses the class-wide cell, the only one an `ocel env`
// command reaches. A named environment's override is readable — `ls` and the
// variables UI show one that exists — but nothing writes one, because no
// runtime path resolves it.
func envCoordinate(slug, key string, opts envOptions) *envv1.Coordinate {
	return &envv1.Coordinate{Slug: slug, Folder: opts.folder, Key: key}
}

func runEnvSet(ctx context.Context, cwd, key, value string, opts envOptions, stdout, stderr io.Writer) error {
	if opts.folder != "" {
		if err := envgate.ValidateFolder(opts.folder); err != nil {
			return err
		}
	}
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		definitions, err := declaredVariables(ctx, cfg, runner, provider, opts, stderr)
		if err != nil {
			return err
		}
		if err := envgate.CheckWritable(definitions, key, opts.folder); err != nil {
			return err
		}

		resp, err := runner.SetValue(ctx, &envv1.SetValueRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Coordinate:      envCoordinate(cfg.Slug, key, opts),
			Value:           value,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Set %s (version %d).\n", describeCell(key, opts), resp.GetMetadata().GetVersion())
		return nil
	})
}

// declaredVariables runs the project's discovery pass to learn what its code
// declares. A write has to know a key's folder scope to reject a cell nothing
// could read, and code is the only authority on scope — the store holds values
// and nothing else, so it cannot answer this from its own side.
func declaredVariables(ctx context.Context, cfg *projectconfig.Config, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, opts envOptions, stderr io.Writer) ([]*resourcesv1.VariableDefinition, error) {
	gate, err := discoverVariables(ctx, cfg, runner, provider, opts, stderr)
	if err != nil {
		return nil, err
	}
	return gate.Definitions(), nil
}

func runEnvLs(ctx context.Context, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		resp, err := runner.ListValues(ctx, &envv1.ListValuesRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Slug:            cfg.Slug,
		})
		if err != nil {
			return err
		}
		renderValues(stdout, resp.GetValues())
		return nil
	})
}

func runEnvGet(ctx context.Context, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		resp, err := runner.GetValue(ctx, &envv1.GetValueRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Coordinate:      envCoordinate(cfg.Slug, key, opts),
			Reveal:          opts.reveal,
		})
		if err != nil {
			return err
		}
		if !resp.GetFound() {
			return fmt.Errorf("no value is set for %s; set one with `ocel env set %s <VALUE>`", describeCell(key, opts), key)
		}

		// A revealed read prints the value and nothing else, so it can be
		// captured straight into a variable.
		if opts.reveal {
			fmt.Fprintln(stdout, resp.GetValue())
			return nil
		}
		m := resp.GetMetadata()
		fmt.Fprintf(stdout, "%s — version %d, %d bytes, updated %s\n", describeCell(key, opts), m.GetVersion(), m.GetSize(), epochOrDash(m.GetUpdatedAt()))
		fmt.Fprintln(stdout, "Pass --reveal to print the value.")
		return nil
	})
}

func runEnvRm(ctx context.Context, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		resp, err := runner.DeleteValue(ctx, &envv1.DeleteValueRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Coordinate:      envCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		if !resp.GetDeleted() {
			fmt.Fprintf(stdout, "No value was set for %s.\n", describeCell(key, opts))
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s.\n", describeCell(key, opts))
		return nil
	})
}

func runEnvHistory(ctx context.Context, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		resp, err := runner.ListVersions(ctx, &envv1.ListVersionsRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Coordinate:      envCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		renderVersions(stdout, describeCell(key, opts), resp.GetVersions())
		return nil
	})
}

// describeCell names a cell the way an operator addressed it, so every message
// identifies exactly which of a key's cells it is about.
func describeCell(key string, opts envOptions) string {
	out := key
	if opts.folder != "" {
		out += " in " + opts.folder
	}
	return out
}

func renderValues(stdout io.Writer, values []*envv1.ValueMetadata) {
	if len(values) == 0 {
		fmt.Fprintln(stdout, "No values set. Set one with `ocel env set <KEY> <VALUE>`.")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tFOLDER\tENVIRONMENT\tVERSION\tBYTES\tUPDATED")
	for _, v := range values {
		c := v.GetCoordinate()
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			c.GetKey(), folderOrRoot(c.GetFolder()), environmentOrClassWide(c.GetEnvironment()),
			v.GetVersion(), v.GetSize(), epochOrDash(v.GetUpdatedAt()))
	}
	_ = tw.Flush()

	// A named ENVIRONMENT is a read-only remnant: no command writes one today,
	// so the column would otherwise name an axis an operator cannot act on.
	for _, v := range values {
		if v.GetCoordinate().GetEnvironment() != "" {
			fmt.Fprintln(stdout, "\nA named ENVIRONMENT holds its own value, written while overrides were still writable. No command reaches those today; `ocel env set` and `ocel env rm` address the class-wide value only.")
			return
		}
	}
}

// renderVersions shows when each version was written and how big it was.
// There is no value column: history says a value changed, not what it was.
func renderVersions(stdout io.Writer, cell string, versions []*envv1.VersionEntry) {
	if len(versions) == 0 {
		fmt.Fprintf(stdout, "No history for %s.\n", cell)
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tCREATED\tBYTES")
	for _, v := range versions {
		fmt.Fprintf(tw, "%d\t%s\t%d\n", v.GetVersion(), epochOrDash(v.GetCreatedAt()), v.GetSize())
	}
	_ = tw.Flush()
}

func folderOrRoot(folder string) string {
	if folder == "" {
		return "(project root)"
	}
	return folder
}

func environmentOrClassWide(environment string) string {
	if environment == "" {
		return "—"
	}
	return environment
}
