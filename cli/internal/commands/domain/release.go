package domain

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runDomainRelease(ctx context.Context, invocation commands.Invocation, cwd string, opts domainOptions, stdout io.Writer, stdin io.Reader) error {
	if err := requirePreviewTier("ocel domain release", opts.preview); err != nil {
		return err
	}
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	policy := consent.NewPlanPolicy("ocel domain release", opts.yes, invocation.StdinIsTerminal(stdin), stdout, stdin)
	if err := policy.Refuse(); err != nil {
		return err
	}

	return invocation.WithProvider(ctx, cfg, policy.Command, commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		run, provider := p.Run, p.Provider
		planning := run.Phase(progressv1.Phase_PHASE_PLAN)
		span := planning.Child(cfg.Slug, progress.Enumerating.Title("what releasing the global preview domain would remove"))
		var plan *planv1.ChangePlan
		err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
			plan, err = client.PlanRemovePreviewWildcard(ctx, &contractv1.PreviewWildcardRequest{
				Tier: environmentv1.Tier_TIER_PREVIEW,
			})
			return err
		})
		span.End(err)
		if err != nil {
			return err
		}
		base := plan.GetSubject()
		if base == "" {
			run.Succeed("No global preview domain is configured")
			return nil
		}

		shown := planning.Plan(fmt.Sprintf("This will release %s and stop serving every project's previews on it", wildcardOf(base)), plan,
			&planv1.Note{Text: "This cannot be undone."})
		granted, err := policy.ConfirmPlanByName(ctx, planning, shown, "domain", base)
		planning.End(err)
		if err != nil {
			return err
		}
		if !granted {
			run.Succeed(fmt.Sprintf("Nothing released: previews stay on %s", wildcardOf(base)))
			return nil
		}

		req := &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Edge: cfg.EdgeSelection()}
		if _, err := providerprocess.Stream(ctx, provider, "RemovePreviewWildcard", req, contractv1connect.ProviderServiceClient.RemovePreviewWildcard); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Released %s", wildcardOf(base)))
		return nil
	})
}

func newReleaseCommand(invocation commands.Invocation) *cobra.Command {
	var opts domainOptions
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Tear down the edge's preview routing and stop serving previews on the global domain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runDomainRelease(cmd.Context(), invocation, cwd, opts, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "Act on the preview tier (required)")
	commands.AddYesFlag(cmd, &opts.yes)
	return commands.Mutates(cmd)
}
