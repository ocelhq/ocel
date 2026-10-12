package previewprotection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "protection",
		Short: "Show, and manage the credentials of, the password that protects this project's previews",
		Long: "Show, and manage the credentials of, the password that protects this project's previews.\n\n" +
			"Whether previews are protected is one switch in the project's config, `previews.protection`, and " +
			"every preview deploy enforces it: turning it on makes the first deploy create a password, turning " +
			"it off opens the previews again. These commands never turn protection on or off. They show the " +
			"password, replace it, and create and revoke the bypass secrets a machine can send instead.\n\n" +
			"A protected preview asks for the password with a login page, or takes it as HTTP Basic credentials, " +
			"or lets through a request that sends a bypass secret in the x-ocel-preview-bypass header. A request " +
			"that fails never reaches the app, and the password never does either.\n\n" +
			"A change is recorded and then published to the edge that serves the project's previews, so it takes " +
			"effect without a deploy. Where an edge cannot be reached the command fails, still prints any " +
			"secret it generated, and `ocel preview protection sync` publishes the recorded credentials again.\n\n" +
			"Run on its own, it says whether protection is on and why, whether a password is set, which bypass " +
			"secrets exist by id, and whether each edge holds the current credentials.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context(), invocation, cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(newPasswordCommand(invocation), newBypassCommand(invocation), newSyncCommand(invocation))
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.PreviewProtectionResult{})
}

func openOptions() commands.OpenOptions {
	return commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features}
}

func loadProject(ctx context.Context, invocation commands.Invocation) (*project.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	return invocation.LoadProject(ctx, cwd)
}

func requireProtection(cfg *project.Project) error {
	if cfg.ProtectPreviews {
		return nil
	}
	return fmt.Errorf("previews.protection is off for %s in %s, so its previews have no credentials to manage: set \"previews\": { \"protection\": true } in the config and deploy a preview",
		cfg.Slug, filepath.Base(cfg.Path))
}

func refuseOpenedPreviews(slug string, resp *contractv1.GetPreviewProtectionResponse) error {
	if !resp.GetRecorded() || resp.GetEnabled() {
		return nil
	}
	return fmt.Errorf("the last preview deploy of %s opened its previews because previews.protection was off, so its kept credentials protect nothing: deploy a preview with previews.protection on to restore them", slug)
}

func readProtection(ctx context.Context, invocation commands.Invocation, cfg *project.Project, command string, reveal bool) (resp *contractv1.GetPreviewProtectionResponse, err error) {
	err = invocation.WithProvider(ctx, cfg, command, openOptions(), func(ctx context.Context, p commands.ProviderRun) error {
		span := p.Check.Child(cfg.Slug, progress.Reading.Title("the preview protection"))
		err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			resp, err = client.GetPreviewProtection(ctx, &contractv1.GetPreviewProtectionRequest{Slug: cfg.Slug, RevealPassword: reveal, Edge: cfg.EdgeSelection()})
			return err
		})
		span.End(err)
		return err
	})
	return resp, err
}

func runStatus(ctx context.Context, invocation commands.Invocation, stdout io.Writer) error {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	resp, err := readProtection(ctx, invocation, cfg, "ocel preview protection", false)
	if err != nil {
		return err
	}
	result := &resultv1.PreviewProtectionResult{
		Slug:            cfg.Slug,
		Enabled:         cfg.ProtectPreviews,
		Recorded:        resp.GetRecorded(),
		BypassSecretIds: resp.GetBypassSecretIds(),
		Edges:           edgeResults(resp.GetEdges()),
	}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	renderStatus(stdout, result, resp.GetEnabled(), filepath.Base(cfg.Path))
	return nil
}

func runBypassList(ctx context.Context, invocation commands.Invocation, stdout io.Writer) error {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	if err := requireProtection(cfg); err != nil {
		return err
	}
	resp, err := readProtection(ctx, invocation, cfg, "ocel preview protection bypass ls", false)
	if err != nil {
		return err
	}
	if err := refuseOpenedPreviews(cfg.Slug, resp); err != nil {
		return err
	}
	result := &resultv1.PreviewBypassSecretListResult{Slug: cfg.Slug, Ids: resp.GetBypassSecretIds()}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	if len(result.GetIds()) == 0 {
		fmt.Fprintf(stdout, "%s has no bypass secrets.\n  → run `ocel preview protection bypass create` to make one\n", result.GetSlug())
		return nil
	}
	for _, id := range result.GetIds() {
		fmt.Fprintln(stdout, id)
	}
	return nil
}

func edgeResults(edges []*progressv1.PreviewEdgePublication) []*resultv1.PreviewEdgeResult {
	results := make([]*resultv1.PreviewEdgeResult, 0, len(edges))
	for _, front := range edges {
		results = append(results, &resultv1.PreviewEdgeResult{Kind: front.GetKind(), Published: front.GetPublished(), Error: front.GetError()})
	}
	return results
}

func renderStatus(out io.Writer, result *resultv1.PreviewProtectionResult, recordEnabled bool, configFile string) {
	if !result.GetEnabled() {
		fmt.Fprintf(out, "Previews of %s are not protected: previews.protection is off in %s.\n", result.GetSlug(), configFile)
		if result.GetRecorded() {
			fmt.Fprintln(out, "  Their password and bypass secrets are kept: turning previews.protection back on restores them.")
		}
		fmt.Fprintln(out, "  → set \"previews\": { \"protection\": true } to protect them")
		return
	}
	fmt.Fprintf(out, "Previews of %s are protected: previews.protection is on in %s.\n", result.GetSlug(), configFile)
	if !result.GetRecorded() {
		fmt.Fprintln(out, "  No password exists yet: the first preview deploy creates it.")
		return
	}
	if !recordEnabled {
		fmt.Fprintln(out, "  The last preview deploy opened them: the next preview deploy protects them again with the kept password and bypass secrets.")
		return
	}
	fmt.Fprintln(out, "  Password: set  → `ocel preview protection password` shows it")
	switch n := len(result.GetBypassSecretIds()); n {
	case 0:
		fmt.Fprintln(out, "  Bypass secrets: none  → `ocel preview protection bypass create` makes one")
	default:
		fmt.Fprintf(out, "  Bypass secrets: %d bypass secret%s (%s)\n", n, plural(n), strings.Join(result.GetBypassSecretIds(), ", "))
	}
	pending := false
	for _, front := range result.GetEdges() {
		switch {
		case front.GetError() != "":
			fmt.Fprintf(out, "  Edge %s: %s\n", front.GetKind(), front.GetError())
		case front.GetPublished():
			fmt.Fprintf(out, "  Edge %s: live\n", front.GetKind())
		default:
			fmt.Fprintf(out, "  Edge %s: pending\n", front.GetKind())
			pending = true
		}
	}
	if len(result.GetEdges()) == 0 {
		fmt.Fprintln(out, "  Edges: none yet; a preview deploy writes the credentials to the edge that serves it")
	}
	if pending {
		fmt.Fprintln(out, "  → `ocel preview protection sync` publishes the recorded credentials again")
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type changeOutcome struct {
	slug      string
	changed   *contractv1.ChangePreviewProtectionResponse
	published *progressv1.PreviewProtectionPublished
}

func (o changeOutcome) edges() []*resultv1.PreviewEdgeResult {
	return edgeResults(o.published.GetEdges())
}

func applyChange(ctx context.Context, invocation commands.Invocation, command string, change *contractv1.ChangePreviewProtectionRequest, headline func(changeOutcome) string) (changeOutcome, error) {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return changeOutcome{}, err
	}
	if err := requireProtection(cfg); err != nil {
		return changeOutcome{}, err
	}
	outcome := changeOutcome{slug: cfg.Slug}
	err = invocation.WithProvider(ctx, cfg, command, openOptions(), func(ctx context.Context, p commands.ProviderRun) error {
		if change != nil {
			change.Slug, change.Edge = cfg.Slug, cfg.EdgeSelection()
			span := p.Check.Child(cfg.Slug, progress.Updating.Title("the recorded preview protection"))
			err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
				outcome.changed, err = client.ChangePreviewProtection(ctx, change)
				return err
			})
			span.End(err)
			if err != nil {
				return err
			}
		}
		p.Check.End(nil)
		result, err := providerprocess.Stream(ctx, p.Provider, "PublishPreviewProtection",
			&contractv1.PublishPreviewProtectionRequest{Slug: cfg.Slug, Edge: cfg.EdgeSelection()},
			contractv1connect.ProviderServiceClient.PublishPreviewProtection)
		outcome.published = result.GetProtection()
		if err != nil {
			return explainUnpublished(err, outcome.published)
		}
		p.Run.Succeed(headline(outcome))
		return nil
	})
	return outcome, err
}

func explainUnpublished(err error, published *progressv1.PreviewProtectionPublished) error {
	var failed []string
	for _, front := range published.GetEdges() {
		if front.GetError() != "" {
			failed = append(failed, fmt.Sprintf("%s: %s", front.GetKind(), front.GetError()))
		}
	}
	if len(failed) == 0 {
		return err
	}
	return fmt.Errorf("%w (%s)", err, strings.Join(failed, "; "))
}

func printSecret(invocation commands.Invocation, stdout io.Writer, secret string, result proto.Message) error {
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	_, err := fmt.Fprintln(stdout, secret)
	return err
}

func printPublication(invocation commands.Invocation, stdout io.Writer, outcome changeOutcome, failure error) error {
	if invocation.Presentation(stdout).Format != terminal.FormatJSON || (failure != nil && outcome.published == nil) {
		return failure
	}
	return failAfterResult(invocation, stdout, failure, terminal.WriteResultJSON(stdout, &resultv1.PreviewPublicationResult{Slug: outcome.slug, Edges: outcome.edges()}))
}

func failAfterResult(invocation commands.Invocation, stdout io.Writer, failure, printed error) error {
	if printed != nil || failure == nil || invocation.Presentation(stdout).Format != terminal.FormatJSON {
		return errors.Join(failure, printed)
	}
	code, reported := exitcode.Of(failure)
	if !reported {
		return failure
	}
	return &exitcode.ExitError{Code: code}
}
