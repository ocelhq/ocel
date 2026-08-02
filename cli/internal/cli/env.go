package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/declcache"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// envOptions are the flags every `ocel env` subcommand shares. folder and
// environment address the cell — the two override axes, one deploy-pinned and
// one live; preview selects the substrate; reveal is the deliberate act that
// prints a value.
type envOptions struct {
	preview     bool
	folder      string
	environment string
	reveal      bool
}

// checkEnvironment refuses an override addressed on a substrate that cannot
// hold one. Production is a single environment, so a value there binds
// class-wide and nothing else: accepting the flag would write a row no
// production function will ever read.
func (o envOptions) checkEnvironment() error {
	if o.environment == "" || o.preview {
		return nil
	}
	return fmt.Errorf("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
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
		c.Flags().StringVar(&envOpts.environment, "environment", "", "Address the override this named preview environment holds instead of the class-wide value")
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

// envCoordinate addresses the cell the flags named: the class-wide value, or
// the override one named preview environment holds.
func envCoordinate(slug, key string, opts envOptions) *envv1.Coordinate {
	return &envv1.Coordinate{Slug: slug, Folder: opts.folder, Key: key, Environment: opts.environment}
}

// namedEnvironments is every preview environment the provider knows about. It
// is the only authority on which names exist: an override is identified by
// exactly the key the runtime derives from its ref, so a name nothing
// enumerates is a name nothing will ever ask for.
func namedEnvironments(ctx context.Context, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, slug string) ([]string, error) {
	resp, err := runner.ListEnvironments(ctx, &deploymentsv1.ListEnvironmentsRequest{
		Options:         []byte(provider.Options),
		ProtocolVersion: manifestbuilder.SchemaVersion,
		Slug:            slug,
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.GetEnvironments()))
	for _, environment := range resp.GetEnvironments() {
		names = append(names, environment.GetIdentity())
	}
	return names, nil
}

func runEnvSet(ctx context.Context, cwd, key, value string, opts envOptions, stdout, stderr io.Writer) error {
	if opts.folder != "" {
		if err := envgate.ValidateFolder(opts.folder); err != nil {
			return err
		}
	}
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		definitions, err := declaredVariables(ctx, cfg, runner, provider, key, opts, stderr)
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

// declaredVariables answers what the project's code declares about key. A
// write has to know a key's folder scope to reject a cell nothing could read,
// and code is the only authority on scope — the store holds values and nothing
// else, so it cannot answer this from its own side.
//
// Learning it means running the discovery pass, which costs a bundle and a node
// process, so the answer is cached per project against a fingerprint of the
// bundled program: a scripted run of writes pays for it once, and the first
// write after the declaring code changes pays for it again. The cache answers
// only for a key it holds a declaration for — a key it is silent about is a
// key the last run's ambient state may have suppressed, and taking that silence
// for "unscoped" is how a root cell for a scoped key gets written. Caching is
// best-effort — a cache that cannot be opened or written just means running
// discovery, which is what this did before it had one.
func declaredVariables(ctx context.Context, cfg *projectconfig.Config, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, key string, opts envOptions, stderr io.Writer) ([]*resourcesv1.VariableDefinition, error) {
	entry, err := deploycollector.Bundle(cfg)
	if err != nil {
		return nil, err
	}
	fingerprint, err := declcache.Fingerprint(entry)
	if err != nil {
		return nil, err
	}

	cache, cacheErr := declcache.Open()
	if cacheErr == nil {
		if definitions, ok := cache.Load(cfg.Dir, fingerprint, key); ok {
			return definitions, nil
		}
	}

	gate := envGate(cfg, runner, provider, opts)
	if _, err := deploycollector.CollectBundled(ctx, cfg, gate, entry, io.Discard, stderr); err != nil {
		return nil, err
	}

	definitions := gate.Definitions()
	if cacheErr == nil {
		_ = cache.Save(cfg.Dir, fingerprint, definitions)
	}
	return definitions, nil
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
		// The enumeration is only asked for when something in the listing has to
		// be judged against it, so a project with no overrides pays nothing. A
		// production listing never asks: named environments are the preview
		// substrate's, so any row addressed at one there is orphaned by
		// definition, and judging it against the preview names would report a
		// row nothing can read as live.
		var environments []string
		if opts.preview && overridden(resp.GetValues()) {
			if environments, err = namedEnvironments(ctx, runner, provider, cfg.Slug); err != nil {
				return err
			}
		}
		renderValues(stdout, resp.GetValues(), environments)
		return nil
	})
}

func overridden(values []*envv1.ValueMetadata) bool {
	return slices.ContainsFunc(values, func(v *envv1.ValueMetadata) bool {
		return v.GetCoordinate().GetEnvironment() != ""
	})
}

func runEnvGet(ctx context.Context, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
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
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
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
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
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
	if opts.environment != "" {
		out += " for " + opts.environment
	}
	return out
}

// renderValues lists the cells a project holds. environments is what the
// provider enumerates, which is what makes an override addressed at a name no
// longer among them an orphan: the value survives, nothing will ever ask for
// it, and saying so is what stops the store quietly accumulating dead rows.
func renderValues(stdout io.Writer, values []*envv1.ValueMetadata, environments []string) {
	if len(values) == 0 {
		fmt.Fprintln(stdout, "No values set. Set one with `ocel env set <KEY> <VALUE>`.")
		return
	}
	orphans := false
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tFOLDER\tENVIRONMENT\tVERSION\tBYTES\tUPDATED")
	for _, v := range values {
		c := v.GetCoordinate()
		environment := environmentOrClassWide(c.GetEnvironment())
		if envgate.Orphaned(environments, c.GetEnvironment()) {
			environment += " (orphaned)"
			orphans = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			c.GetKey(), folderOrRoot(c.GetFolder()), environment,
			v.GetVersion(), v.GetSize(), epochOrDash(v.GetUpdatedAt()))
	}
	_ = tw.Flush()

	if orphans {
		fmt.Fprintln(stdout, "\nAn orphaned override belongs to an environment that no longer exists, so nothing will ever read it. Remove one with `ocel env rm <KEY> --preview --environment <ENVIRONMENT>`.")
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
