package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/edge"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func refuseMissingPreviewDomain(cfg *project.Project, wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, hostnameRequired bool, check *run.Span) error {
	declared := cfg.Domains.Preview
	base := wildcard.GetBaseDomain()
	configName := filepath.Base(cfg.Path)

	switch {
	case declared == "" && base == "" && !hostnameRequired:
		check.Say("Serving previews on the address each app's release is given")

	case declared == "" && base == "":
		return fmt.Errorf("this project declares no preview domain and this bootstrap has no global one, so a preview deploy has nowhere to serve: "+
			"add a project-level domains.preview wildcard (e.g. `domains: { preview: \"*.preview.acme.com\" }`) to %s, "+
			"or run `ocel domain use '*.preview.acme.com' --preview` once to serve every project's previews on one shared wildcard — "+
			"a preview domain binds to the whole project, which serves every app and every preview under that one wildcard, so it is never declared per app",
			configName)

	case declared == "":
		if err := checkGlobalPreviewDomain(wildcard, id, configName); err != nil {
			return err
		}
		check.Say(fmt.Sprintf("Serving previews on global *.%s", base))

	case declared == edge.PreviewWildcard(base):
		check.Say(fmt.Sprintf("Serving previews on project-level %s, also the global preview domain", declared))

	case base != "":
		check.Say(fmt.Sprintf("Serving previews on project-level %s; global *.%s ignored", declared, base))
	}
	return nil
}

func previewAppNames(cfg *project.Project) []string {
	names := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		names = append(names, app.Name)
	}
	return names
}

func checkGlobalPreviewDomain(wildcard *contractv1.PreviewWildcard, id *contractv1.Identity, configName string) error {
	base := wildcard.GetBaseDomain()
	if want, have := wildcard.GetEdgeScope(), id.GetEdgeScope(); want != "" && have != "" && want != have {
		return fmt.Errorf("the global preview domain *.%s lives in edge account %s, but this deploy is authenticated to account %s: "+
			"the wildcard can only be served from the account that owns it — "+
			"re-scope this run's edge credentials to %s, or declare this project's own domains.preview in %s",
			base, want, have, want, configName)
	}
	if !wildcard.GetRouteInstalled() {
		return fmt.Errorf("the global preview domain *.%s is recorded, but its wildcard route is not installed, so nothing would answer a preview hostname: "+
			"run `ocel domain use '*.%s' --preview` to reinstall the edge's preview routing and reclaim the wildcard",
			base, base)
	}
	return nil
}

func RemovePreview(ctx context.Context, dependencies Dependencies, cwd, name string, yes bool, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	env, err := resolvePreviewEnvironment(dependencies, cwd, name, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED)
	if err != nil {
		return err
	}

	policy := consent.NewPolicy("ocel preview rm", yes, dependencies.CanAsk(stdin), stdout, stdin)

	var removed *consolev1.EnvironmentEvent
	err = dependencies.WithProvider(ctx, cfg, "ocel preview rm", commands.OpenOptions{Intent: commands.UseParts, Tier: environmentv1.Tier_TIER_PREVIEW}, func(ctx context.Context, p commands.ProviderRun) error {
		run, check, provider := p.Run, p.Check, p.Provider
		recorded, err := readPreview(ctx, provider, cfg.Slug, env)
		if err != nil {
			return err
		}
		if question := composeTeardownQuestion(recorded); question != "" {
			if err := policy.RefuseQuestion(question, "before it tears the preview down"); err != nil {
				return err
			}
			proceed, err := policy.Confirm(ctx, check, consent.Guard{
				ID:       consent.GuardPersistentPreviewRemoval,
				Question: question,
				Action:   fmt.Sprintf("removing the persistent preview %q", env.GetIdentity()),
			})
			if err != nil {
				return err
			}
			if !proceed {
				run.Succeed(fmt.Sprintf("Nothing torn down: preview %s stays", env.GetIdentity()))
				return nil
			}
		}
		env.Lifecycle = recorded.GetLifecycle()

		registry, warning := readiness.RemovalRegistry(cfg)
		if warning != "" {
			check.Warn(warning)
		}
		check.End(nil)

		req := &contractv1.RemoveEnvironmentRequest{
			Environment:     env,
			Slug:            cfg.Slug,
			Edge:            cfg.EdgeSelection(),
			ProjectRegistry: registry,
		}
		if _, err := providerprocess.Stream(ctx, provider, "RemoveEnvironment", req, contractv1connect.ProviderServiceClient.RemoveEnvironment); err != nil {
			return err
		}
		removed = deployreport.NewEnvironmentEvent(consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED, env, run.TraceID(), time.Now(), cfg.Dir, os.Getenv)
		run.Succeed(fmt.Sprintf("Tore down preview %s of %s", env.GetIdentity(), cfg.Slug))
		return nil
	})
	if removed != nil {
		dependencies.Console.ReportEnvironmentEvent(ctx, cfg.Dir, removed, stderr)
	}
	return err
}

func readPreview(ctx context.Context, provider *providerprocess.Provider, slug string, env *environmentv1.Environment) (recorded *contractv1.PreviewEnvironment, err error) {
	err = provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
		got, err := client.GetEnvironment(ctx, &contractv1.GetEnvironmentRequest{Slug: slug, Environment: env})
		recorded = got.GetEnvironment()
		return err
	})
	return recorded, err
}

func composeTeardownQuestion(recorded *contractv1.PreviewEnvironment) string {
	if recorded.GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT {
		return ""
	}
	return fmt.Sprintf("Tear down the persistent preview %q?", recorded.GetIdentity())
}

func ListPreviews(ctx context.Context, dependencies Dependencies, cwd string, stdout io.Writer) error {
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	previews, err := listPreviews(ctx, dependencies, cfg)
	if err != nil {
		return err
	}
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, previewListResult(previews))
	}
	renderEnvironments(stdout, previews)
	return nil
}

func previewListResult(previews []*contractv1.PreviewEnvironment) *resultv1.PreviewListResult {
	result := &resultv1.PreviewListResult{Previews: make([]*resultv1.PreviewSummary, 0, len(previews))}
	for _, preview := range previews {
		result.Previews = append(result.Previews, &resultv1.PreviewSummary{
			Identity:  preview.GetIdentity(),
			Lifecycle: preview.GetLifecycle(),
			Label:     preview.GetLabel(),
			CreatedAt: terminal.EpochRFC3339(preview.GetCreatedAt()),
			AliasUrls: preview.GetAliasUrls(),
		})
	}
	return result
}

func listPreviews(ctx context.Context, dependencies Dependencies, cfg *project.Project) (previews []*contractv1.PreviewEnvironment, err error) {
	err = dependencies.WithProvider(ctx, cfg, "ocel preview ls", commands.OpenOptions{}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		return p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
			listed, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: cfg.Slug})
			previews = listed.GetEnvironments()
			return err
		})
	})
	return previews, err
}

func PrunePreview(ctx context.Context, dependencies Dependencies, cwd, name string, keep int, yes bool, stdout io.Writer, stdin io.Reader) error {
	env, err := resolvePreviewEnvironment(dependencies, cwd, name, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED)
	if err != nil {
		return err
	}

	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	policy := consent.NewPlanPolicy("ocel preview prune", yes, dependencies.CanAsk(stdin), stdout, stdin)
	if err := policy.Refuse(); err != nil {
		return err
	}

	return dependencies.WithProvider(ctx, cfg, policy.Command, commands.OpenOptions{Intent: commands.UseParts, Tier: environmentv1.Tier_TIER_PREVIEW}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		plan := p.Run.Phase(progressv1.Phase_PHASE_PLAN)
		plan.Say(fmt.Sprintf("This will delete every deployment of preview %s but the newest %d and the live one; none of them can be rolled back to afterwards", env.GetIdentity(), keep))
		registry, warning := readiness.RemovalRegistry(cfg)
		if warning != "" {
			plan.Warn(warning)
		}
		granted, err := policy.ConfirmPlan(ctx, plan, nil, fmt.Sprintf("Delete the older deployments of preview %s?", env.GetIdentity()))
		plan.End(err)
		if err != nil {
			return err
		}
		if !granted {
			p.Run.Succeed(fmt.Sprintf("Nothing deleted: preview %s keeps every deployment", env.GetIdentity()))
			return nil
		}

		req := &contractv1.RemoveStalePromotionsRequest{
			Slug:            cfg.Slug,
			KeepN:           int32(keep),
			Environment:     env,
			Edge:            cfg.EdgeSelection(),
			ProjectRegistry: registry,
		}
		if _, err := providerprocess.Stream(ctx, p.Provider, "RemoveStalePromotions", req, contractv1connect.ProviderServiceClient.RemoveStalePromotions); err != nil {
			return err
		}
		p.Run.Succeed(fmt.Sprintf("Pruned the promotions of preview %s down to the newest %d", env.GetIdentity(), keep))
		return nil
	})
}

func renderEnvironments(stdout io.Writer, envs []*contractv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No previews.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\tcreated %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			formatValueOrDash(e.GetLabel()),
			formatValueOrDash(strings.Join(e.GetAliasUrls(), " ")),
			terminal.EpochDate(e.GetCreatedAt()),
		)
	}
}

func lifecycleTag(l environmentv1.Lifecycle) string {
	switch l {
	case environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL:
		return "ephemeral"
	case environmentv1.Lifecycle_LIFECYCLE_PERSISTENT:
		return "persistent"
	default:
		return "—"
	}
}

func formatValueOrDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}
