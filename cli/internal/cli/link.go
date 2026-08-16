package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

const defaultLinkOwner = "cli"

type linkOptions struct {
	preview     bool
	environment string
	owner       string
}

func (o linkOptions) substrate() envOptions {
	return envOptions{preview: o.preview, environment: o.environment}
}

func (o linkOptions) publisher() string {
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
		"your own cloud account and are reached through the provider, never by the CLI directly.",
}

var linkSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Publish one link, read as JSON on stdin",
	Long: "Publish one link, read as JSON on stdin.\n\n" +
		"The link is a links.v1.Link in protobuf JSON, and it carries its own name, so " +
		"there is nothing to name on the command line:\n\n" +
		"  ocel link set < link.json\n\n" +
		"A name belongs to whoever published it. Publishing over a name another publisher " +
		"holds is refused rather than handing every app bound to that name another " +
		"resource's values; pass --owner to publish as that publisher.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkSet(ctx, defaultDeps(), cwd, cmd.InOrStdin(), linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var linkRmCmd = &cobra.Command{
	Use:   "rm <NAME>",
	Short: "Remove a link, whatever published it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkRm(ctx, defaultDeps(), cwd, args[0], linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

var linkLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the published links, without revealing what they hold",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withEnvCommand(cmd, func(ctx context.Context, cwd string) error {
			return runLinkLs(ctx, defaultDeps(), cwd, linkOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
		})
	},
}

func init() {
	for _, c := range []*cobra.Command{linkSetCmd, linkRmCmd, linkLsCmd} {
		c.Flags().BoolVar(&linkOpts.preview, "preview", false, "Act on the preview substrate instead of production")
		c.Flags().StringVar(&linkOpts.environment, "environment", "", "Address the link this named preview environment holds instead of the class-wide one")
		linkCmd.AddCommand(c)
	}
	linkSetCmd.Flags().StringVar(&linkOpts.owner, "owner", defaultLinkOwner, "Publish under this publisher's name")
	rootCmd.AddCommand(linkCmd)
}

func runLinkSet(ctx context.Context, d deps, cwd string, stdin io.Reader, opts linkOptions, stdout, stderr io.Writer) error {
	link, err := decodeLink(stdin)
	if err != nil {
		return err
	}
	owner := opts.publisher()
	return envSession(ctx, d, cwd, opts.substrate(), stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.SetLink(ctx, &deploymentsv1.SetLinkRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
			Class:           envClass(opts.substrate()),
			Environment:     opts.environment,
			Link:            link,
			Owner:           owner,
		})
		if err != nil {
			return err
		}
		if logFormat() == logFormatJSON {
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

func runLinkRm(ctx context.Context, d deps, cwd, name string, opts linkOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts.substrate(), stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.RemoveLink(ctx, &deploymentsv1.RemoveLinkRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
			Class:           envClass(opts.substrate()),
			Environment:     opts.environment,
			Name:            name,
		})
		if err != nil {
			return err
		}
		if logFormat() == logFormatJSON {
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

func runLinkLs(ctx context.Context, d deps, cwd string, opts linkOptions, stdout, stderr io.Writer) error {
	return envSession(ctx, d, cwd, opts.substrate(), stdout, stderr, func(runner *providerrunner.Runner, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor) error {
		client, err := runner.Deployments()
		if err != nil {
			return err
		}
		resp, err := client.ListLinks(ctx, &deploymentsv1.ListLinksRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Slug:            cfg.Slug,
			Class:           envClass(opts.substrate()),
			Environment:     opts.environment,
		})
		if err != nil {
			return err
		}
		if logFormat() == logFormatJSON {
			return writeLinkJSON(stdout, linkListReport{Links: linkReports(resp.GetLinks())})
		}
		renderLinks(stdout, resp.GetLinks())
		return nil
	})
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

func linkReports(links []*deploymentsv1.LinkSummary) []linkReport {
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

func renderLinks(stdout io.Writer, links []*deploymentsv1.LinkSummary) {
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
