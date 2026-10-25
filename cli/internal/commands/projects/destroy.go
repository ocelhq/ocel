package projects

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const destroyCommand = "ocel projects destroy"

type destroyOptions struct {
	slug string
	tier string
	yes  bool
	dry  bool
}

func newDestroyCommand(invocation commands.Invocation) *cobra.Command {
	var opts destroyOptions
	cmd := &cobra.Command{
		Use:   "destroy [slug]",
		Short: "Permanently destroy a project in both tiers",
		Long: "Permanently destroy a project: in production, what the edge serves it with and the hostnames bound " +
			"to it, its resources (databases and buckets, including all their data) and every app; in preview, " +
			"its whole preview footprint, leaving the account-level preview bootstrap intact. --tier limits it to one.\n\n" +
			"Destroys this directory's project unless given another's slug, which it destroys through the edge that " +
			"project recorded.\n\n" +
			"It is irreversible: every run prints what it would destroy in each tier, then asks once for the project " +
			"name; --dry prints it and stops.\n\n" +
			"An automated caller that must tear a project down unattended passes --yes, or sets " +
			consent.BypassEnv + " to the project name — and only that name. Any other value is not a bypass.",
		Example: "  $ ocel projects destroy\n" +
			"  $ ocel projects destroy --tier preview --dry\n" +
			"  $ ocel projects destroy blog --yes",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.slug = args[0]
			}
			return runDestroy(cmd.Context(), invocation, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	cmd.Flags().StringVar(&opts.tier, "tier", "", "Destroy only the project's production or preview")
	commands.AddYesFlag(cmd, &opts.yes)
	commands.AddDryFlag(cmd, &opts.dry, "Print what would be destroyed and stop, destroying nothing")
	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}

func requestedTiers(flag string) ([]environmentv1.Tier, error) {
	switch flag {
	case "":
		return []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW}, nil
	case "production", "prod":
		return []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}, nil
	case "preview":
		return []environmentv1.Tier{environmentv1.Tier_TIER_PREVIEW}, nil
	}
	return nil, fmt.Errorf("--tier is production or preview, not %q", flag)
}

type removalTarget struct {
	slug     string
	edge     *contractv1.EdgeSelection
	registry *contractv1.ImageRegistry
	warning  string
	recorded []environmentv1.Tier
}

type tierRemoval struct {
	tier      environmentv1.Tier
	provider  *providerprocess.Provider
	consented *planv1.ChangePlan
}

func runDestroy(ctx context.Context, invocation commands.Invocation, opts destroyOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	tiers, err := requestedTiers(opts.tier)
	if err != nil {
		return err
	}
	resolved, err := resolveTarget(ctx, invocation)
	if err != nil {
		return err
	}
	cfg := resolved.Project
	slug := cmp.Or(opts.slug, cfg.Slug)
	bypass, notice, err := consent.Bypass{
		Noun:        "project",
		Subject:     slug,
		Action:      "destroying it",
		Verb:        "destroyed",
		Yes:         opts.yes,
		DryRun:      opts.dry,
		Interactive: invocation.CanAsk(stdin),
	}.Granted()
	if err != nil {
		return err
	}
	policy := consent.NewPlanPolicy(destroyCommand, opts.yes || bypass, invocation.CanAsk(stdin), stdout, stdin)
	policy.DryRun = opts.dry
	policy.UnattendedRemedy = fmt.Sprintf("pass --yes, or set %s to the project name", consent.BypassEnv)

	destroyed, err := destroyProject(ctx, invocation, resolved, slug, tiers, policy, notice)
	if slug == cfg.Slug {
		for _, event := range destroyed {
			invocation.Console.ReportEnvironmentEvent(ctx, cfg.Dir, event, stderr)
		}
	}
	return err
}

func destroyProject(ctx context.Context, invocation commands.Invocation, resolved commands.Target, slug string, tiers []environmentv1.Tier, policy consent.Policy, bypassNotice string) (destroyed []*consolev1.EnvironmentEvent, err error) {
	cfg := resolved.Project
	if !resolved.IsCloud() {
		if _, err := cfg.RequireProvider(); err != nil {
			return nil, err
		}
	}
	if err := policy.Refuse(); err != nil {
		return nil, err
	}

	ctx, running, err := invocation.Events.Begin(ctx, policy.Command, cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer running.End(&err)

	check := running.Phase(progressv1.Phase_PHASE_CHECK)
	if bypassNotice != "" {
		check.Warn(bypassNotice)
	}
	account, err := openAccount(ctx, invocation, check, resolved, policy.DryRun)
	if err != nil {
		check.End(err)
		return nil, err
	}
	defer func() {
		if account.provider != nil {
			account.provider.Close()
		}
	}()
	target, err := readRemovalTarget(ctx, check, account.provider, cfg, slug)
	if err != nil {
		check.End(err)
		return nil, err
	}
	if target.warning != "" {
		check.Warn(target.warning)
	}
	var removals []*tierRemoval
	defer func() {
		for _, removal := range removals {
			removal.provider.Close()
		}
	}()
	subject := cfg
	if slug != cfg.Slug {
		subject = asRecorded(cfg, target)
	}
	for _, tier := range tiers {
		if !slices.Contains(target.recorded, tier) {
			check.Say(fmt.Sprintf("nothing of %s in %s", slug, readiness.TierName(tier)))
			continue
		}
		if slug == cfg.Slug && tier == account.tier {
			if err := readiness.RefuseUnready(account.read.Response, account.provider, cfg, readiness.Request{Tier: tier, Require: readiness.Parts, DryRun: policy.DryRun}); err != nil {
				check.End(err)
				return nil, err
			}
			removals = append(removals, &tierRemoval{tier: tier, provider: account.provider})
			account.provider = nil
			continue
		}
		provider, _, err := invocation.OpenProvider(ctx, check, subject, commands.OpenOptions{Intent: commands.UseParts, Tier: tier, Policy: policy, DryRun: policy.DryRun, Cloud: resolved.Cloud, CloudScopes: []cloudv1.Scope{cloudv1.Scope_SCOPE_APPS_WRITE}})
		if err != nil {
			check.End(err)
			return nil, err
		}
		removals = append(removals, &tierRemoval{tier: tier, provider: provider})
	}
	if account.provider != nil {
		account.provider.Close()
		account.provider = nil
	}
	check.End(nil)
	if len(removals) == 0 {
		running.Succeed(fmt.Sprintf("Nothing to destroy: %s has nothing in %s", slug, tierNames(tiers)))
		return nil, nil
	}

	planning := running.Phase(progressv1.Phase_PHASE_PLAN)
	confirmed := &planv1.ChangePlan{Subject: slug}
	var planned []*tierRemoval
	for _, removal := range removals {
		plan, err := planRemoval(ctx, planning, target, removal.tier, removal.provider)
		if err != nil {
			planning.End(err)
			return nil, err
		}
		if len(plan.GetGroups()) == 0 {
			planning.Say(fmt.Sprintf("nothing of %s in %s", slug, readiness.TierName(removal.tier)))
			continue
		}
		removal.consented = showDestroyPlan(planning, slug, removal.tier, plan)
		confirmed.Groups = append(confirmed.Groups, removal.consented.GetGroups()...)
		planned = append(planned, removal)
	}
	if len(planned) == 0 {
		planning.End(nil)
		running.Succeed(fmt.Sprintf("Nothing to destroy: %s has nothing in %s", slug, tierNames(tiers)))
		return nil, nil
	}
	if policy.DryRun {
		planning.Say("Run without --dry to destroy.")
		planning.End(nil)
		running.Succeed(fmt.Sprintf("Planned the destroy of %s", slug))
		return nil, nil
	}
	granted, err := policy.ConfirmPlanByName(ctx, planning, confirmed, "project name", slug)
	planning.End(err)
	if err != nil {
		return nil, err
	}
	if !granted {
		running.Succeed(fmt.Sprintf("Nothing destroyed: %s stays", slug))
		return nil, nil
	}

	for _, removal := range planned {
		req := &contractv1.ProjectRequest{
			Slug:            slug,
			Environment:     removalEnvironment(removal.tier),
			Edge:            target.edge,
			Consented:       removal.consented,
			ProjectRegistry: target.registry,
		}
		if _, err := providerprocess.Stream(ctx, removal.provider, "RemoveProject", req, contractv1connect.ProviderServiceClient.RemoveProject); err != nil {
			return destroyed, err
		}
		destroyed = append(destroyed, deployreport.NewEnvironmentEvent(consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED, &environmentv1.Environment{Tier: removal.tier}, running.TraceID(), time.Now(), cfg.Dir, os.Getenv))
	}
	running.Succeed(fmt.Sprintf("Destroyed project %s", slug))
	return destroyed, nil
}

type accountCheck struct {
	provider *providerprocess.Provider
	read     readiness.Preflight
	tier     environmentv1.Tier
}

func openAccount(ctx context.Context, invocation commands.Invocation, check *run.Span, resolved commands.Target, dry bool) (accountCheck, error) {
	open := accountOptions(resolved)
	open.CloudScopes = []cloudv1.Scope{cloudv1.Scope_SCOPE_APPS_WRITE}
	open.DryRun = dry
	provider, read, err := invocation.OpenProvider(ctx, check, resolved.Project, open)
	return accountCheck{provider: provider, read: read, tier: open.Tier}, err
}

func readRemovalTarget(ctx context.Context, check *run.Span, provider *providerprocess.Provider, cfg *project.Project, slug string) (removalTarget, error) {
	span := check.Child(slug, progress.Reading.Title("where "+slug+" is deployed"))
	var recorded *contractv1.GetProjectResponse
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		recorded, err = client.GetProject(ctx, &contractv1.GetProjectRequest{Slug: slug})
		return err
	})
	span.End(err)
	if err != nil {
		return removalTarget{}, err
	}
	target := removalTarget{slug: slug, recorded: recorded.GetTiers()}
	if len(target.recorded) == 0 {
		return target, nil
	}
	if slug == cfg.Slug {
		target.edge = cfg.EdgeSelection()
		target.registry, target.warning = readiness.RemovalRegistry(cfg)
		return target, nil
	}
	target.edge = recorded.GetEdge()
	if registry := recorded.GetRegistry(); registry.GetServer() != "" {
		target.warning = fmt.Sprintf("The images %s pushed to %s stay there until they are deleted in the registry: "+
			"its password is never recorded, so run `%s` from %s's directory for the removal to delete them",
			slug, registry.GetServer(), destroyCommand, slug)
	}
	return target, nil
}

func asRecorded(cfg *project.Project, target removalTarget) *project.Project {
	recorded := &project.Project{
		Slug:     target.slug,
		Provider: cfg.Provider,
		Edge:     &project.Edge{Kind: edge.Kind(target.edge.GetKind()), Options: target.edge.GetOptions()},
		Dir:      cfg.Dir,
		Path:     cfg.Path,
	}
	for _, need := range target.edge.GetAllowDegraded() {
		recorded.AllowDegraded = append(recorded.AllowDegraded, edge.Need(need))
	}
	if dns := target.edge.GetDns(); dns != nil {
		recorded.DNS = &project.DNS{Kind: dns.GetKind(), Zone: dns.GetZone()}
	}
	return recorded
}

func planRemoval(ctx context.Context, planning *run.Span, target removalTarget, tier environmentv1.Tier, provider *providerprocess.Provider) (*planv1.ChangePlan, error) {
	span := planning.Child(target.slug, progress.Enumerating.Title("what to destroy in "+placeOf(tier)))
	var plan *planv1.ChangePlan
	err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		plan, err = client.PlanRemoveProject(ctx, &contractv1.ProjectRequest{
			Slug:        target.slug,
			Environment: removalEnvironment(tier),
			Edge:        target.edge,
		})
		return err
	})
	span.End(err)
	return plan, err
}

func removalEnvironment(tier environmentv1.Tier) *environmentv1.Environment {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return &environmentv1.Environment{Tier: tier}
	}
	return nil
}

func placeOf(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "any preview"
	}
	return "production"
}

func tierNames(tiers []environmentv1.Tier) string {
	names := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		names = append(names, readiness.TierName(tier))
	}
	return strings.Join(names, " or ")
}

func showDestroyPlan(planning *run.Span, slug string, tier environmentv1.Tier, plan *planv1.ChangePlan) *planv1.ChangePlan {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return planning.Plan(fmt.Sprintf("This will permanently destroy the ENTIRE preview footprint of project %q", slug), plan,
			&planv1.Note{Text: "The account-level preview bootstrap is left intact. This cannot be undone."})
	}
	return planning.Plan(fmt.Sprintf("This will permanently destroy production project %q", slug), plan,
		&planv1.Note{Text: "This cannot be undone."})
}
