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
//
// envSession runs it for every subcommand, before anything is resolved or
// spawned, so a subcommand added later cannot quietly omit it — and so the
// refusal costs nothing: there is no store to ask about a coordinate that
// cannot exist.
func (o envOptions) checkEnvironment() error {
	if o.environment == "" || o.preview {
		return nil
	}
	return fmt.Errorf("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}

var envOpts envOptions

// envRefOptions addresses the cell a reference reads: the project owning the
// value, the folder within it, and the name it is set under there. Each is
// omitted for the common case — the same project, its root, and the same name —
// so pointing one project's key at another's is one flag.
//
// There is no environment component: a reference resolves against the target's
// class-wide value, and there is no other address to offer.
type envRefOptions struct {
	project string
	folder  string
	key     string
}

var envRefOpts envRefOptions

// target is the coordinate a reference points at, with each component defaulted
// to the consuming cell's own.
func (o envRefOptions) target(slug, key string) *envv1.Coordinate {
	project, name := o.project, o.key
	if project == "" {
		project = slug
	}
	if name == "" {
		name = key
	}
	return &envv1.Coordinate{Slug: project, Folder: o.folder, Key: name}
}

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

var envRefCmd = &cobra.Command{
	Use:   "ref <KEY>",
	Short: "Point a value at one owned elsewhere instead of copying it",
	Long: "Point a value at one owned elsewhere instead of copying it.\n\n" +
		"The cell then holds an address. It is read through to the value behind it every " +
		"time, so an edit at the source reaches every consumer with nothing to re-run, and " +
		"the value itself is only ever edited where it is set. Without --target-project the " +
		"target is this project, which is how one folder reads another's value.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvRef(ctx, cwd, args[0], envOpts, envRefOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envRefsCmd = &cobra.Command{
	Use:   "refs <KEY>",
	Short: "List what references a value, before an edit changes it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvRefs(ctx, cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
	for _, c := range []*cobra.Command{envLsCmd, envSetCmd, envGetCmd, envRmCmd, envHistoryCmd, envRefCmd, envRefsCmd} {
		c.Flags().BoolVar(&envOpts.preview, "preview", false, "Act on the preview substrate instead of production")
		envCmd.AddCommand(c)
	}
	for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd, envRefCmd, envRefsCmd} {
		c.Flags().StringVar(&envOpts.folder, "folder", "", "Address the value in this folder (e.g. /checkout) instead of the project root")
	}
	for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd, envRefCmd} {
		c.Flags().StringVar(&envOpts.environment, "environment", "", "Address the override this named preview environment holds instead of the class-wide value")
	}
	// The target is class-wide, always: a named environment belongs to the
	// project holding the reference and means nothing in the target's namespace,
	// so there is no flag to address one. Which cell is being asked about in
	// `refs` is class-wide for the same reason — nothing can point at an
	// override, so nothing points at one.
	envRefCmd.Flags().StringVar(&envRefOpts.project, "target-project", "", "Read the value owned by this project instead of this one")
	envRefCmd.Flags().StringVar(&envRefOpts.folder, "target-folder", "", "Read the value in this folder of the target project instead of its root")
	envRefCmd.Flags().StringVar(&envRefOpts.key, "target-key", "", "Read the target's value under this name; without it, the same name")
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
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
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
		if target := m.GetTarget(); target != nil {
			fmt.Fprintf(stdout, "%s references %s — version %d, pointed %s\n", describeCell(key, opts), describeCoordinate(target), m.GetVersion(), epochOrDash(m.GetUpdatedAt()))
			fmt.Fprintln(stdout, "Pass --reveal to print the value it reads. Edit that value where it is set.")
			return nil
		}
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

// runEnvRef points a cell at a value owned elsewhere. The cell is checked
// against what the code declares exactly as a written value is: what a
// reference changes is where the value comes from, not whether the project has
// anywhere to put one.
func runEnvRef(ctx context.Context, cwd, key string, opts envOptions, ref envRefOptions, stdout, stderr io.Writer) error {
	for _, folder := range []string{opts.folder, ref.folder} {
		if folder == "" {
			continue
		}
		if err := envgate.ValidateFolder(folder); err != nil {
			return err
		}
	}
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		definitions, err := declaredVariables(ctx, cfg, runner, provider, key, opts, stderr)
		if err != nil {
			return err
		}
		if err := envgate.CheckWritable(definitions, key, opts.folder); err != nil {
			return err
		}
		target := ref.target(cfg.Slug, key)
		resp, err := runner.SetReference(ctx, &envv1.SetReferenceRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Coordinate:      envCoordinate(cfg.Slug, key, opts),
			Target:          target,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s now reads %s (version %d).\n", describeCell(key, opts), describeCoordinate(target), resp.GetMetadata().GetVersion())
		return nil
	})
}

// runEnvRefs answers what reads a value, which is the blast radius of editing
// it. Nothing is inferred from an empty answer: a value nothing references is
// the ordinary case, not a mistake.
func runEnvRefs(ctx context.Context, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		resp, err := runner.ListReferences(ctx, &envv1.ListReferencesRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           envClass(opts),
			Coordinate:      envCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		renderReferences(stdout, describeCell(key, opts), resp.GetReferences())
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
	if opts.environment != "" {
		out += " for " + opts.environment
	}
	return out
}

// describeCoordinate names a cell that may belong to another project, which is
// what a reference's two ends have to be told apart by.
func describeCoordinate(c *envv1.Coordinate) string {
	out := c.GetSlug() + "/" + c.GetKey()
	if c.GetFolder() != "" {
		out += " in " + c.GetFolder()
	}
	if c.GetEnvironment() != "" {
		out += " for " + c.GetEnvironment()
	}
	return out
}

// renderReferences lists what reads a value, which is what editing it changes.
func renderReferences(stdout io.Writer, cell string, references []*envv1.Coordinate) {
	if len(references) == 0 {
		fmt.Fprintf(stdout, "Nothing references %s.\n", cell)
		return
	}
	fmt.Fprintf(stdout, "%d referencing %s:\n", len(references), cell)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tKEY\tFOLDER\tENVIRONMENT")
	for _, c := range references {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			c.GetSlug(), c.GetKey(), folderOrRoot(c.GetFolder()), environmentOrClassWide(c.GetEnvironment()))
	}
	_ = tw.Flush()
	fmt.Fprintln(stdout, "\nEditing this value changes what every one of them reads.")
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
	fmt.Fprintln(tw, "KEY\tFOLDER\tENVIRONMENT\tVERSION\tBYTES\tUPDATED\tSOURCE")
	for _, v := range values {
		c := v.GetCoordinate()
		environment := environmentOrClassWide(c.GetEnvironment())
		if envgate.Orphaned(environments, c.GetEnvironment()) {
			environment += " (orphaned)"
			orphans = true
		}
		// A reference has no size of its own, so the byte count is left blank
		// rather than reported as zero, which would read as an empty value.
		size := fmt.Sprint(v.GetSize())
		source := "—"
		if target := v.GetTarget(); target != nil {
			size, source = "—", describeCoordinate(target)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			c.GetKey(), folderOrRoot(c.GetFolder()), environment,
			v.GetVersion(), size, epochOrDash(v.GetUpdatedAt()), source)
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
