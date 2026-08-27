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
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

const defaultLinkOwner = "cli"

type linkOptions struct {
	preview     bool
	environment string
	owner       string
}

func (o linkOptions) checkEnvironment() error {
	if o.environment == "" || o.preview {
		return nil
	}
	return fmt.Errorf("--environment addresses one preview environment's override, and production has a single environment; pass --preview, or leave --environment off to address the production value")
}

func (o linkOptions) tier() environmentv1.Tier {
	if o.preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func (o linkOptions) ownerOrDefault() string {
	if o.owner == "" {
		return defaultLinkOwner
	}
	return o.owner
}

var linkOpts linkOptions

var linkCmd = &cobra.Command{
	Use:   "link",
	Short: "Manage the links this project's apps resolve",
	Long: "Manage the links this project's apps resolve.\n\n" +
		"A link is one resource an app reaches — its address, its credentials and the " +
		"permissions that go with it — published under a name apps bind to. Records live in " +
		"your own provider account and are reached through the provider, never by the CLI directly.",
}

var linkSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Publish one link, read as JSON on stdin",
	Long: "Publish one link, read as JSON on stdin.\n\n" +
		"The link is a common.links.v1.Link in protobuf JSON, and it carries its own name, so " +
		"there is nothing to name on the command line:\n\n" +
		"  ocel link set < link.json\n\n" +
		"A name belongs to whoever published it. Publishing over a name another publisher " +
		"holds is refused rather than handing every app bound to that name another " +
		"resource's values; pass --owner to publish as that publisher.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withLinkCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkSet(ctx, newDeps(), cwd, cmd.InOrStdin(), linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var linkRmCmd = &cobra.Command{
	Use:   "rm <NAME>",
	Short: "Remove a link, whatever published it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withLinkCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkRm(ctx, newDeps(), cwd, args[0], linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var linkLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the published links, without revealing what they hold",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withLinkCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkLs(ctx, newDeps(), cwd, linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var linkGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Write the transform types for the links published to one coordinate",
	Long: "Write the transform types for the links published to one coordinate.\n\n" +
		"Reads the records published to production, or to the preview coordinate --preview and " +
		"--environment name, and writes " + linkTypesFileName + " beside your ocel config. The file " +
		"names each record and the properties it carries, so `links.<name>.<property>` in a transform " +
		"is checked where it is written instead of at the deploy. Check it in, and run this again when " +
		"what you publish changes.\n\n" +
		"Unlike `ocel generate`, this reads the published records: it logs in and runs the provider.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withLinkCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkGenerate(ctx, newDeps(), cwd, linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{linkSetCmd, linkRmCmd, linkLsCmd, linkGenerateCmd} {
		c.Flags().BoolVar(&linkOpts.preview, "preview", false, "Act on the preview bootstrap instead of production")
		c.Flags().StringVar(&linkOpts.environment, "environment", "", "Address the link this named preview environment holds instead of the one bound to all environments")
		linkCmd.AddCommand(c)
	}
	linkSetCmd.Flags().StringVar(&linkOpts.owner, "owner", defaultLinkOwner, "Publish under this publisher's name")
	rootCmd.AddCommand(linkCmd)
}

func withLinkCommand(cmd *cobra.Command, run func(context.Context, string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
	defer stop()
	return run(ctx, cwd)
}

func withLinkProvider(ctx context.Context, deps cmddeps.Deps, cwd string, opts linkOptions, stderr io.Writer, drive func(*provider.Runner, *projectconfig.Config) error) error {
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

func runLinkSet(ctx context.Context, deps cmddeps.Deps, cwd string, stdin io.Reader, opts linkOptions, stdout, stderr io.Writer) error {
	link, err := decodeLink(stdin)
	if err != nil {
		return err
	}
	owner := opts.ownerOrDefault()
	return withLinkProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.SetLink(ctx, &envvarsv1.SetLinkRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Link:        link,
			Owner:       owner,
		})
		if err != nil {
			return err
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeLinkJSON(stdout, linkSetReport{Name: link.GetName(), Owner: owner, Version: resp.GetVersion()})
		}
		fmt.Fprintf(stdout, "Published %s as %s (version %d).\n", describeLink(link.GetName(), opts), owner, resp.GetVersion())
		return nil
	})
}

func decodeLink(stdin io.Reader) (*linksv1.Link, error) {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read the link on stdin: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("nothing came in on stdin; `ocel link set` reads one link as protobuf JSON, so pipe it in: `ocel link set < link.json`")
	}
	link := &linksv1.Link{}
	if err := protojson.Unmarshal(raw, link); err != nil {
		return nil, fmt.Errorf("read the link on stdin: %w", err)
	}
	return link, nil
}

func runLinkRm(ctx context.Context, deps cmddeps.Deps, cwd, name string, opts linkOptions, stdout, stderr io.Writer) error {
	return withLinkProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.RemoveLink(ctx, &envvarsv1.RemoveLinkRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
			Name:        name,
		})
		if err != nil {
			return err
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeLinkJSON(stdout, linkRemoveReport{Name: name, Removed: resp.GetRemoved()})
		}
		if !resp.GetRemoved() {
			fmt.Fprintf(stdout, "No link named %s is published.\n", describeLink(name, opts))
			return nil
		}
		fmt.Fprintf(stdout, "Removed %s.\n", describeLink(name, opts))
		return nil
	})
}

func runLinkLs(ctx context.Context, deps cmddeps.Deps, cwd string, opts linkOptions, stdout, stderr io.Writer) error {
	return withLinkProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.ListLinks(ctx, &envvarsv1.ListLinksRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
		})
		if err != nil {
			return err
		}
		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeLinkJSON(stdout, linkListReport{Links: linkReports(resp.GetLinks())})
		}
		renderLinks(stdout, resp.GetLinks())
		return nil
	})
}

func runLinkGenerate(ctx context.Context, deps cmddeps.Deps, cwd string, opts linkOptions, stdout, stderr io.Writer) error {
	return withLinkProvider(ctx, deps, cwd, opts, stderr, func(runner *provider.Runner, cfg *projectconfig.Config) error {
		client, err := runner.Vars()
		if err != nil {
			return err
		}
		resp, err := client.ListLinks(ctx, &envvarsv1.ListLinksRequest{
			Slug:        cfg.Slug,
			Tier:        opts.tier(),
			Environment: opts.environment,
		})
		if err != nil {
			return err
		}

		path := filepath.Join(cfg.Dir, linkTypesFileName)
		if err := os.WriteFile(path, []byte(renderLinkTypes(runner.Package(), describeLinkCoordinate(opts), resp.GetLinks())), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", linkTypesFileName, err)
		}

		if deps.Presentation(stdout).Format == runui.FormatJSON {
			return writeLinkJSON(stdout, linkGenerateReport{Path: path, Links: linkReports(resp.GetLinks())})
		}
		if len(resp.GetLinks()) == 0 {
			fmt.Fprintf(stdout, "Nothing is published to %s; wrote %s, which names no record and so leaves no link name open.\n", describeLinkCoordinate(opts), path)
			return nil
		}
		fmt.Fprintf(stdout, "Wrote %s from the %d links published to %s.\n", path, len(resp.GetLinks()), describeLinkCoordinate(opts))
		return nil
	})
}

func describeLinkCoordinate(opts linkOptions) string {
	if !opts.preview {
		return "production"
	}
	if opts.environment == "" {
		return "preview"
	}
	return "the preview environment " + opts.environment
}

type linkGenerateReport struct {
	Path  string       `json:"path"`
	Links []linkReport `json:"links"`
}

type linkSetReport struct {
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	Version uint64 `json:"version"`
}

type linkRemoveReport struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

type linkListReport struct {
	Links []linkReport `json:"links"`
}

type linkReport struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Source  string `json:"source"`
	Owner   string `json:"owner"`
	Version uint64 `json:"version"`
}

func linkReports(links []*envvarsv1.LinkSummary) []linkReport {
	out := make([]linkReport, 0, len(links))
	for _, l := range links {
		out = append(out, linkReport{
			Name:    l.GetName(),
			Type:    linkTypeName(l.GetType()),
			Source:  l.GetSource(),
			Owner:   l.GetOwner(),
			Version: l.GetVersion(),
		})
	}
	return out
}

func writeLinkJSON(stdout io.Writer, report any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func renderLinks(stdout io.Writer, links []*envvarsv1.LinkSummary) {
	if len(links) == 0 {
		fmt.Fprintln(stdout, "No links published. Publish one with `ocel link set < link.json`.")
		return
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSOURCE\tOWNER\tVERSION")
	for _, l := range links {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			l.GetName(), linkTypeName(l.GetType()), sourceOrDash(l.GetSource()), l.GetOwner(), l.GetVersion())
	}
	_ = tw.Flush()
}

func linkTypeName(t linksv1.LinkType) string {
	return strings.ToLower(naming.EnvFragment(t))
}

func sourceOrDash(source string) string {
	if source == "" {
		return "—"
	}
	return source
}

func describeLink(name string, opts linkOptions) string {
	if opts.environment != "" {
		return name + " for " + opts.environment
	}
	return name
}
