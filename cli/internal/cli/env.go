package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/declcache"
	"github.com/ocelhq/ocel/cli/internal/deploycollector"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type envOptions struct {
	preview     bool
	folder      string
	environment string
	reveal      bool
}

func (o envOptions) checkEnvironment() error {
	if o.environment == "" || o.preview {
		return nil
	}
	return fmt.Errorf("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}

var envOpts envOptions

type envRefOptions struct {
	project string
	folder  string
	key     string
}

var envRefOpts envRefOptions

func (o envRefOptions) target(slug, key string) *envvarsv1.Coordinate {
	project, name := o.project, o.key
	if project == "" {
		project = slug
	}
	if name == "" {
		name = key
	}
	return &envvarsv1.Coordinate{Slug: project, Folder: o.folder, Key: name}
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
			return runEnvLs(ctx, defaultDeps(), cwd, envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envSetCmd = &cobra.Command{
	Use:   "set <KEY> <VALUE>",
	Short: "Set a value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvSet(ctx, defaultDeps(), cwd, args[0], args[1], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envGetCmd = &cobra.Command{
	Use:   "get <KEY>",
	Short: "Show one value's metadata, or the value itself with --reveal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvGet(ctx, defaultDeps(), cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envRmCmd = &cobra.Command{
	Use:   "rm <KEY>",
	Short: "Remove a value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvRm(ctx, defaultDeps(), cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
			return runEnvRef(ctx, defaultDeps(), cwd, args[0], envOpts, envRefOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envRefsCmd = &cobra.Command{
	Use:   "refs <KEY>",
	Short: "List what references a value, before an edit changes it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvRefs(ctx, defaultDeps(), cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var envHistoryCmd = &cobra.Command{
	Use:   "history <KEY>",
	Short: "Show a value's change history, newest first",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runEnvHistory(ctx, defaultDeps(), cwd, args[0], envOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{envLsCmd, envSetCmd, envGetCmd, envRmCmd, envHistoryCmd, envRefCmd, envRefsCmd} {
		c.Flags().BoolVar(&envOpts.preview, "preview", false, "Act on the preview bootstrap instead of production")
		envCmd.AddCommand(c)
	}
	for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd, envRefCmd, envRefsCmd} {
		c.Flags().StringVar(&envOpts.folder, "folder", "", "Address the value in this folder (e.g. /checkout) instead of the project root")
	}
	for _, c := range []*cobra.Command{envSetCmd, envGetCmd, envRmCmd, envHistoryCmd, envRefCmd} {
		c.Flags().StringVar(&envOpts.environment, "environment", "", "Address the override this named preview environment holds instead of the class-wide value")
	}
	envRefCmd.Flags().StringVar(&envRefOpts.project, "target-project", "", "Read the value owned by this project instead of this one")
	envRefCmd.Flags().StringVar(&envRefOpts.folder, "target-folder", "", "Read the value in this folder of the target project instead of its root")
	envRefCmd.Flags().StringVar(&envRefOpts.key, "target-key", "", "Read the target's value under this name; without it, the same name")
	envGetCmd.Flags().BoolVar(&envOpts.reveal, "reveal", false, "Print the value itself; without it only metadata is shown")
	rootCmd.AddCommand(envCmd)
}

func withEnvCommand(cmd *cobra.Command, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
	defer stop()
	return run(ctx, cwd)
}

func envSession(ctx context.Context, d deps, cwd string, opts envOptions, stdout, stderr io.Writer, drive func(*providerrunner.Runner, *projectconfig.Config, *projectconfig.ProviderDescriptor) error) error {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}
	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	tier, hint := environmentv1.Tier_TIER_PRODUCTION, "ocel bootstrap"
	if opts.preview {
		tier, hint = environmentv1.Tier_TIER_PREVIEW, "ocel bootstrap --preview"
	}

	return runProviderSession(ctx, d, cfg, provider, stderr, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightSchema(ctx, d, runner, cfg, tier, hint, stderr); err != nil {
			return err
		}
		return drive(runner, cfg, provider)
	})
}

func envTier(opts envOptions) environmentv1.Tier {
	if opts.preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func envCoordinate(slug, key string, opts envOptions) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{Slug: slug, Folder: opts.folder, Key: key, Environment: opts.environment}
}

func namedEnvironments(ctx context.Context, runner *providerrunner.Runner, slug string) ([]string, error) {
	client, err := runner.Deployments()
	if err != nil {
		return nil, err
	}
	resp, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{
		Slug: slug,
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

func runEnvSet(ctx context.Context, d deps, cwd, key, value string, opts envOptions, stdout, stderr io.Writer) error {
	if opts.folder != "" {
		if err := envgate.ValidateFolder(opts.folder); err != nil {
			return err
		}
	}
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		definitions, err := declaredVariables(ctx, d, cfg, runner, key, opts, stderr)
		if err != nil {
			return err
		}
		if err := envgate.CheckWritable(definitions, key, opts.folder); err != nil {
			return err
		}
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.SetValue(ctx, &envvarsv1.SetValueRequest{
			Tier:       envTier(opts),
			Coordinate: envCoordinate(cfg.Slug, key, opts),
			Value:      value,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Set %s (version %d).\n", describeCell(key, opts), resp.GetMetadata().GetVersion())
		return nil
	})
}

func declaredVariables(ctx context.Context, d deps, cfg *projectconfig.Config, runner *providerrunner.Runner, key string, opts envOptions, stderr io.Writer) ([]*resourcesv1.VariableDefinition, error) {
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

	gate := envGate(cfg, runner, opts)
	if _, err := deploycollector.CollectBundled(ctx, cfg, gate, entry, io.Discard, stderr); err != nil {
		return nil, err
	}

	definitions := gate.Definitions()
	if cacheErr == nil {
		_ = cache.Save(cfg.Dir, fingerprint, definitions)
	}
	return definitions, nil
}

func runEnvLs(ctx context.Context, d deps, cwd string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.ListValues(ctx, &envvarsv1.ListValuesRequest{
			Tier: envTier(opts),
			Slug: cfg.Slug,
		})
		if err != nil {
			return err
		}
		var environments []string
		if opts.preview && overridden(resp.GetValues()) {
			if environments, err = namedEnvironments(ctx, runner, cfg.Slug); err != nil {
				return err
			}
		}
		renderValues(stdout, resp.GetValues(), environments)
		return nil
	})
}

func overridden(values []*envvarsv1.ValueMetadata) bool {
	return slices.ContainsFunc(values, func(v *envvarsv1.ValueMetadata) bool {
		return v.GetCoordinate().GetEnvironment() != ""
	})
}

func runEnvGet(ctx context.Context, d deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.GetValue(ctx, &envvarsv1.GetValueRequest{
			Tier:       envTier(opts),
			Coordinate: envCoordinate(cfg.Slug, key, opts),
			Reveal:     opts.reveal,
		})
		if err != nil {
			return err
		}
		if !resp.GetFound() {
			return fmt.Errorf("no value is set for %s; set one with `ocel env set %s <VALUE>`", describeCell(key, opts), key)
		}

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

func runEnvRm(ctx context.Context, d deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.DeleteValue(ctx, &envvarsv1.DeleteValueRequest{
			Tier:       envTier(opts),
			Coordinate: envCoordinate(cfg.Slug, key, opts),
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

func runEnvRef(ctx context.Context, d deps, cwd, key string, opts envOptions, ref envRefOptions, stdout, stderr io.Writer) error {
	for _, folder := range []string{opts.folder, ref.folder} {
		if folder == "" {
			continue
		}
		if err := envgate.ValidateFolder(folder); err != nil {
			return err
		}
	}
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		definitions, err := declaredVariables(ctx, d, cfg, runner, key, opts, stderr)
		if err != nil {
			return err
		}
		if err := envgate.CheckWritable(definitions, key, opts.folder); err != nil {
			return err
		}
		target := ref.target(cfg.Slug, key)
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.SetReference(ctx, &envvarsv1.SetReferenceRequest{
			Tier:       envTier(opts),
			Coordinate: envCoordinate(cfg.Slug, key, opts),
			Target:     target,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s now reads %s (version %d).\n", describeCell(key, opts), describeCoordinate(target), resp.GetMetadata().GetVersion())
		return nil
	})
}

func runEnvRefs(ctx context.Context, d deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.ListReferences(ctx, &envvarsv1.ListReferencesRequest{
			Tier:       envTier(opts),
			Coordinate: envCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		renderReferences(stdout, describeCell(key, opts), resp.GetReferences())
		return nil
	})
}

func runEnvHistory(ctx context.Context, d deps, cwd, key string, opts envOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts, stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, _ *projectconfig.ProviderDescriptor) error {
		vars, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := vars.ListVersions(ctx, &envvarsv1.ListVersionsRequest{
			Tier:       envTier(opts),
			Coordinate: envCoordinate(cfg.Slug, key, opts),
		})
		if err != nil {
			return err
		}
		renderVersions(stdout, describeCell(key, opts), resp.GetVersions())
		return nil
	})
}

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

func describeCoordinate(c *envvarsv1.Coordinate) string {
	out := c.GetSlug() + "/" + c.GetKey()
	if c.GetFolder() != "" {
		out += " in " + c.GetFolder()
	}
	if c.GetEnvironment() != "" {
		out += " for " + c.GetEnvironment()
	}
	return out
}

func renderReferences(stdout io.Writer, cell string, references []*envvarsv1.Coordinate) {
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

func renderValues(stdout io.Writer, values []*envvarsv1.ValueMetadata, environments []string) {
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

func renderVersions(stdout io.Writer, cell string, versions []*envvarsv1.VersionEntry) {
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
