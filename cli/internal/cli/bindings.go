package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/preflight"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const defaultBindingOwner = "cli"

type bindingsOptions struct {
	preview     bool
	environment string
	owner       string
}

func (o bindingsOptions) checkEnvironment() error {
	if o.environment == "" || o.preview {
		return nil
	}
	return fmt.Errorf("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}

func (o bindingsOptions) tier() environmentv1.Tier {
	if o.preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func (o bindingsOptions) ownerOrDefault() string {
	if o.owner == "" {
		return defaultBindingOwner
	}
	return o.owner
}

var bindingsOpts bindingsOptions

var bindingsCmd = &cobra.Command{
	Use:   "bindings",
	Short: "Manage the bindings this project's apps resolve",
	Long: "Manage the bindings this project's apps resolve.\n\n" +
		"A binding is one resource an app reaches — its address, its credentials and the " +
		"permissions that go with it — published under a name apps bind to. Records live in " +
		"your own provider account and are reached through the provider, never by the CLI directly.",
}

var bindingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Publish one binding, read as JSON on stdin",
	Long: "Publish one binding, read as JSON on stdin.\n\n" +
		"The binding is a common.bindings.v1.Binding in protobuf JSON, and it carries its own name, so " +
		"there is nothing to name on the command line:\n\n" +
		"  ocel bindings set < binding.json\n\n" +
		"A name belongs to whoever published it. Publishing over a name another publisher " +
		"holds is refused rather than handing every app bound to that name another " +
		"resource's values; pass --owner to publish as that publisher.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
			return runBindingsSet(ctx, newDeps(), cwd, cmd.InOrStdin(), bindingsOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var bindingsRmCmd = &cobra.Command{
	Use:   "rm <NAME>",
	Short: "Remove a binding, whatever published it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
			return runBindingsRm(ctx, newDeps(), cwd, args[0], bindingsOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var bindingsLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the published bindings, without revealing what they hold",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
			return runBindingsLs(ctx, newDeps(), cwd, bindingsOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var bindingsGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Write the transform types for the bindings published to one coordinate",
	Long: "Write the transform types for the bindings published to one coordinate.\n\n" +
		"Reads the records published to production, or to the preview coordinate --preview and " +
		"--environment name, and writes " + bindingTypesFileName + " beside your ocel config. The file " +
		"names each record and the properties it carries, so `bindings.<name>.<property>` in a transform " +
		"is checked where it is written instead of at the deploy. Check it in, and run this again when " +
		"what you publish changes.\n\n" +
		"This reads the published records, so it logs in and runs the provider.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withBindingCommand(cmd, func(ctx context.Context, cwd string) error {
			return runBindingsGenerate(ctx, newDeps(), cwd, bindingsOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{bindingsSetCmd, bindingsRmCmd, bindingsLsCmd, bindingsGenerateCmd} {
		c.Flags().BoolVar(&bindingsOpts.preview, "preview", false, "Act on the preview bootstrap instead of production")
		c.Flags().StringVar(&bindingsOpts.environment, "environment", "", "Address the binding this named preview environment holds instead of the one bound to all environments")
		bindingsCmd.AddCommand(c)
	}
	bindingsSetCmd.Flags().StringVar(&bindingsOpts.owner, "owner", defaultBindingOwner, "Publish under this publisher's name")
	rootCmd.AddCommand(bindingsCmd)
}

func withBindingCommand(cmd *cobra.Command, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
	defer stop()
	return run(ctx, cwd)
}

func withBindingProvider(ctx context.Context, deps cmddeps.Deps, cwd string, opts bindingsOptions, stderr io.Writer, drive func(*provider.Runner, *projectconfig.Config) error) error {
	if err := opts.checkEnvironment(); err != nil {
		return err
	}
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	hint := "ocel bootstrap production"
	if opts.preview {
		hint = "ocel bootstrap preview"
	}

	return provider.Drive(ctx, cfg, stderr, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		if err := preflight.Credentials(ctx, runui.Plain(deps.Presentation(stderr), stderr), runner, cfg, opts.tier(), hint); err != nil {
			return err
		}
		return drive(runner, cfg)
	})
}

func runBindingsSet(ctx context.Context, deps cmddeps.Deps, cwd string, stdin io.Reader, opts bindingsOptions, stdout, stderr io.Writer) error {
	binding, err := decodeBinding(stdin)
	if err != nil {
		return err
	}
	owner := opts.ownerOrDefault()
	return withBindingProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.SetBinding(ctx, &envvarsv1.SetBindingRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Binding:     binding,
			Owner:       owner,
		})
		if err != nil {
			return err
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeBindingJSON(stdout, bindingSetReport{Name: binding.GetName(), Owner: owner, Version: resp.GetVersion()})
		}
		fmt.Fprintf(stdout, "Published %s as %s (version %d).\n", describeBinding(binding.GetName(), opts), owner, resp.GetVersion())
		return nil
	})
}

func decodeBinding(stdin io.Reader) (*bindingsv1.Binding, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read the binding on stdin: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("nothing came in on stdin; `ocel bindings set` reads one binding as protobuf JSON, so pipe it in: `ocel bindings set < binding.json`")
	}
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal(raw, binding); err != nil {
		return nil, fmt.Errorf("read the binding on stdin: %w", err)
	}
	return binding, nil
}

func runBindingsRm(ctx context.Context, deps cmddeps.Deps, cwd, name string, opts bindingsOptions, stdout, stderr io.Writer) error {
	return withBindingProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.RemoveBinding(ctx, &envvarsv1.RemoveBindingRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Name:        name,
		})
		if err != nil {
			return err
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeBindingJSON(stdout, bindingRemoveReport{Name: name, Removed: resp.GetRemoved()})
		}
		if !resp.GetRemoved() {
			fmt.Fprintf(stdout, "No binding named %s is published.\n", describeBinding(name, opts))
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s.\n", describeBinding(name, opts))
		return nil
	})
}

func runBindingsLs(ctx context.Context, deps cmddeps.Deps, cwd string, opts bindingsOptions, stdout, stderr io.Writer) error {
	return withBindingProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
		})
		if err != nil {
			return err
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeBindingJSON(stdout, bindingListReport{Bindings: bindingReports(resp.GetBindings())})
		}
		renderBindings(stdout, resp.GetBindings())
		return nil
	})
}

func runBindingsGenerate(ctx context.Context, deps cmddeps.Deps, cwd string, opts bindingsOptions, stdout, stderr io.Writer) error {
	return withBindingProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
		})
		if err != nil {
			return err
		}

		path := filepath.Join(cfg.Dir, bindingTypesFileName)
		if err := os.WriteFile(path, []byte(renderBindingTypes(runner.Name(), describeBindingCoordinate(opts), resp.GetBindings())), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", bindingTypesFileName, err)
		}

		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeBindingJSON(stdout, bindingGenerateReport{Path: path, Bindings: bindingReports(resp.GetBindings())})
		}
		if len(resp.GetBindings()) == 0 {
			fmt.Fprintf(stdout, "Nothing is published to %s; wrote %s, which names no record and so leaves no binding name open.\n", describeBindingCoordinate(opts), path)
			return nil
		}
		fmt.Fprintf(stdout, "Wrote %s from the %d bindings published to %s.\n", path, len(resp.GetBindings()), describeBindingCoordinate(opts))
		return nil
	})
}

func describeBindingCoordinate(opts bindingsOptions) string {
	if !opts.preview {
		return "production"
	}
	if opts.environment == "" {
		return "preview"
	}
	return "the preview environment " + opts.environment
}

type bindingGenerateReport struct {
	Path     string          `json:"path"`
	Bindings []bindingReport `json:"bindings"`
}

type bindingSetReport struct {
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	Version uint64 `json:"version"`
}

type bindingRemoveReport struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

type bindingListReport struct {
	Bindings []bindingReport `json:"bindings"`
}

type bindingReport struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Source  string `json:"source"`
	Owner   string `json:"owner"`
	Version uint64 `json:"version"`
}

func bindingReports(bindings []*envvarsv1.BindingSummary) []bindingReport {
	out := make([]bindingReport, 0, len(bindings))
	for _, l := range bindings {
		out = append(out, bindingReport{
			Name:    l.GetName(),
			Type:    bindingTypeName(l.GetType()),
			Source:  l.GetSource(),
			Owner:   l.GetOwner(),
			Version: l.GetVersion(),
		})
	}
	return out
}

func writeBindingJSON(stdout io.Writer, report any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func renderBindings(stdout io.Writer, bindings []*envvarsv1.BindingSummary) {
	if len(bindings) == 0 {
		fmt.Fprintln(stdout, "No bindings published. Publish one with `ocel bindings set < binding.json`.")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSOURCE\tOWNER\tVERSION")
	for _, l := range bindings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			l.GetName(), bindingTypeName(l.GetType()), sourceOrDash(l.GetSource()), l.GetOwner(), l.GetVersion())
	}
	_ = tw.Flush()
}

func bindingTypeName(t bindingsv1.BindingType) string {
	return strings.ToLower(naming.EnvFragment(t))
}

func sourceOrDash(source string) string {
	if source == "" {
		return "—"
	}
	return source
}

func describeBinding(name string, opts bindingsOptions) string {
	if opts.environment != "" {
		return name + " for " + opts.environment
	}
	return name
}
