package protection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/english"

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
		Short: "Show, and manage the credentials of, the password that protects production and previews",
		Long: "Show, and manage the credentials of, the password that protects this project's production and its previews.\n\n" +
			"Whether a tier is protected is one switch per tier in the project's config, `protection.production` and " +
			"`protection.preview`, and every deploy of a tier enforces its switch: turning it on makes the first deploy " +
			"create a password, turning it off opens the tier again and keeps the credentials. These commands never turn " +
			"protection on or off. They show the password, replace it, and create and revoke the bypass secrets a machine " +
			"can send instead. Each tier has credentials of its own: a preview password never opens production.\n\n" +
			"A protected tier asks for the password with a login page, or takes it as HTTP Basic credentials, or lets " +
			"through a request that sends a bypass secret in the x-ocel-bypass header. A request that fails never " +
			"reaches the app, and the password never does either.\n\n" +
			"Commands act on production unless given `--preview`. A change is recorded and then published to the edge " +
			"that serves the tier, so it takes effect without a deploy. Where an edge cannot be reached the command fails, " +
			"still prints any secret it generated, and `ocel protection sync` publishes the recorded credentials again.\n\n" +
			"Run on its own, it says for both tiers whether protection is on and why, whether a password is set, which " +
			"bypass secrets exist by id, and whether each edge holds the current credentials.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.Context(), invocation, cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(newPasswordCommand(invocation), newBypassCommand(invocation), newSyncCommand(invocation))
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.ProtectionResult{})
}

type target struct {
	preview bool
}

func (t *target) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&t.preview, "preview", false, "Act on the previews instead of production")
}

func (t target) tier() environmentv1.Tier {
	return commands.ChooseTier(t.preview)
}

func (t target) name() string {
	return readiness.TierName(t.tier())
}

func (t target) flag() string {
	if t.preview {
		return " --preview"
	}
	return ""
}

func configKey(tier environmentv1.Tier) string {
	return "protection." + readiness.TierName(tier)
}

func deployFor(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "a preview deploy"
	}
	return "`ocel deploy`"
}

func statusSubject(tier environmentv1.Tier, slug string) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "Previews of " + slug + " are"
	}
	return "Production of " + slug + " is"
}

func describeTier(tier environmentv1.Tier, slug string) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "the previews of " + slug
	}
	return "the production of " + slug
}

func openOptions(tier environmentv1.Tier) commands.OpenOptions {
	return commands.OpenOptions{Tier: tier, Require: readiness.Parts}
}

func loadProject(ctx context.Context, invocation commands.Invocation) (*project.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	return invocation.LoadProject(ctx, cwd)
}

func requireProtection(cfg *project.Project, t target) error {
	tier := t.tier()
	if cfg.Protection.Protects(tier) {
		return nil
	}
	return fmt.Errorf("%s is off for %s in %s, so there are no %s credentials to manage: set \"protection\": { \"%s\": true } in the config and run %s",
		configKey(tier), cfg.Slug, filepath.Base(cfg.Path), t.name(), t.name(), deployFor(tier))
}

func refuseOpened(t target, slug string, resp *contractv1.GetProtectionResponse) error {
	if !resp.GetRecorded() || resp.GetEnabled() {
		return nil
	}
	tier := t.tier()
	return fmt.Errorf("the last %s deploy of %s opened %s because %s was off, so its kept credentials protect nothing: run %s with %s on to restore them",
		t.name(), slug, describeTier(tier, slug), configKey(tier), deployFor(tier), configKey(tier))
}

func readProtection(ctx context.Context, invocation commands.Invocation, cfg *project.Project, command string, t target, reveal bool) (resp *contractv1.GetProtectionResponse, err error) {
	err = invocation.WithProvider(ctx, cfg, command, openOptions(t.tier()), func(ctx context.Context, p commands.ProviderRun) error {
		span := p.Check.Child(cfg.Slug, progress.Reading.Title("the "+t.name()+" protection"))
		resp, err = getProtection(ctx, p, cfg, t.tier(), reveal)
		span.End(err)
		return err
	})
	return resp, err
}

func getProtection(ctx context.Context, p commands.ProviderRun, cfg *project.Project, tier environmentv1.Tier, reveal bool) (resp *contractv1.GetProtectionResponse, err error) {
	err = p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		resp, err = client.GetProtection(ctx, &contractv1.GetProtectionRequest{Tier: tier, Slug: cfg.Slug, RevealPassword: reveal, Edge: cfg.EdgeSelection()})
		return err
	})
	return resp, err
}

type tierReading struct {
	tier          environmentv1.Tier
	recordEnabled bool
	result        *resultv1.TierProtection
}

func runStatus(ctx context.Context, invocation commands.Invocation, stdout io.Writer) error {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	var readings []tierReading
	opened := commands.OpenOptions{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: readiness.Credentials}
	err = invocation.WithProvider(ctx, cfg, "ocel protection", opened, func(ctx context.Context, p commands.ProviderRun) error {
		for _, tier := range []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW} {
			span := p.Check.Child(cfg.Slug, progress.Reading.Title("the "+readiness.TierName(tier)+" protection"))
			resp, err := getProtection(ctx, p, cfg, tier, false)
			span.End(err)
			if err != nil {
				return err
			}
			readings = append(readings, tierReading{tier: tier, recordEnabled: resp.GetEnabled(), result: &resultv1.TierProtection{
				Tier:            tier,
				Enabled:         cfg.Protection.Protects(tier),
				Recorded:        resp.GetRecorded(),
				BypassSecretIds: resp.GetBypassSecretIds(),
				Edges:           edgeResults(resp.GetEdges()),
			}})
		}
		return nil
	})
	if err != nil {
		return err
	}
	result := &resultv1.ProtectionResult{Slug: cfg.Slug}
	for _, reading := range readings {
		result.Tiers = append(result.Tiers, reading.result)
	}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	for i, reading := range readings {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		renderStatus(stdout, cfg.Slug, reading, filepath.Base(cfg.Path))
	}
	return nil
}

func runBypassList(ctx context.Context, invocation commands.Invocation, t target, stdout io.Writer) error {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	if err := requireProtection(cfg, t); err != nil {
		return err
	}
	resp, err := readProtection(ctx, invocation, cfg, "ocel protection bypass ls", t, false)
	if err != nil {
		return err
	}
	if err := refuseOpened(t, cfg.Slug, resp); err != nil {
		return err
	}
	result := &resultv1.BypassSecretListResult{Slug: cfg.Slug, Tier: t.tier(), Ids: resp.GetBypassSecretIds()}
	if invocation.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	if len(result.GetIds()) == 0 {
		fmt.Fprintf(stdout, "%s has no %s bypass secrets.\n  → run `ocel protection bypass create%s` to make one\n", result.GetSlug(), t.name(), t.flag())
		return nil
	}
	for _, id := range result.GetIds() {
		fmt.Fprintln(stdout, id)
	}
	return nil
}

func edgeResults(edges []*progressv1.EdgePublication) []*resultv1.EdgeResult {
	results := make([]*resultv1.EdgeResult, 0, len(edges))
	for _, front := range edges {
		results = append(results, &resultv1.EdgeResult{Kind: front.GetKind(), Published: front.GetPublished(), Error: front.GetError()})
	}
	return results
}

func renderStatus(out io.Writer, slug string, reading tierReading, configFile string) {
	tier, result := reading.tier, reading.result
	name := readiness.TierName(tier)
	if !result.GetEnabled() {
		fmt.Fprintf(out, "%s not protected: %s is off in %s.\n", statusSubject(tier, slug), configKey(tier), configFile)
		if result.GetRecorded() {
			fmt.Fprintf(out, "  Its password and bypass secrets are kept: turning %s back on restores them.\n", configKey(tier))
		}
		fmt.Fprintf(out, "  → set \"protection\": { \"%s\": true } to protect it\n", name)
		return
	}
	fmt.Fprintf(out, "%s protected: %s is on in %s.\n", statusSubject(tier, slug), configKey(tier), configFile)
	flag := target{preview: tier == environmentv1.Tier_TIER_PREVIEW}.flag()
	if !result.GetRecorded() {
		fmt.Fprintf(out, "  No password exists yet: the first %s deploy creates it.\n", name)
		return
	}
	if !reading.recordEnabled {
		fmt.Fprintf(out, "  The last %s deploy opened it: the next %s deploy protects it again with the kept password and bypass secrets.\n", name, name)
		return
	}
	fmt.Fprintf(out, "  Password: set  → `ocel protection password%s` shows it\n", flag)
	switch n := len(result.GetBypassSecretIds()); n {
	case 0:
		fmt.Fprintf(out, "  Bypass secrets: none  → `ocel protection bypass create%s` makes one\n", flag)
	default:
		fmt.Fprintf(out, "  Bypass secrets: %s (%s)\n", english.Count(n, "bypass secret"), strings.Join(result.GetBypassSecretIds(), ", "))
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
		fmt.Fprintf(out, "  Edges: none yet; a %s deploy writes the credentials to the edge that serves it\n", name)
	}
	if pending {
		fmt.Fprintf(out, "  → `ocel protection sync%s` publishes the recorded credentials again\n", flag)
	}
}

type changeOutcome struct {
	slug      string
	tier      environmentv1.Tier
	changed   *contractv1.ChangeProtectionResponse
	published *progressv1.ProtectionPublished
}

func (o changeOutcome) edges() []*resultv1.EdgeResult {
	return edgeResults(o.published.GetEdges())
}

func applyChange(ctx context.Context, invocation commands.Invocation, t target, command string, change *contractv1.ChangeProtectionRequest, headline func(changeOutcome) string) (changeOutcome, error) {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return changeOutcome{}, err
	}
	if err := requireProtection(cfg, t); err != nil {
		return changeOutcome{}, err
	}
	outcome := changeOutcome{slug: cfg.Slug, tier: t.tier()}
	err = invocation.WithProvider(ctx, cfg, command, openOptions(t.tier()), func(ctx context.Context, p commands.ProviderRun) error {
		if change != nil {
			change.Tier, change.Slug, change.Edge = t.tier(), cfg.Slug, cfg.EdgeSelection()
			span := p.Check.Child(cfg.Slug, progress.Updating.Title("the recorded "+t.name()+" protection"))
			err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
				outcome.changed, err = client.ChangeProtection(ctx, change)
				return err
			})
			span.End(err)
			if err != nil {
				return err
			}
		}
		p.Check.End(nil)
		result, err := providerprocess.Stream(ctx, p.Provider, "PublishProtection",
			&contractv1.PublishProtectionRequest{Tier: t.tier(), Slug: cfg.Slug, Edge: cfg.EdgeSelection()},
			contractv1connect.ProviderServiceClient.PublishProtection)
		outcome.published = result.GetProtection()
		if err != nil {
			return explainUnpublished(err, outcome.published)
		}
		p.Run.Succeed(headline(outcome))
		return nil
	})
	return outcome, err
}

func explainUnpublished(err error, published *progressv1.ProtectionPublished) error {
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
	return failAfterResult(invocation, stdout, failure, terminal.WriteResultJSON(stdout, &resultv1.PublicationResult{Slug: outcome.slug, Tier: outcome.tier, Edges: outcome.edges()}))
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
