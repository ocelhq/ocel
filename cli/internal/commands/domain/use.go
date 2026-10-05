package domain

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func runDomainUse(ctx context.Context, invocation commands.Invocation, cwd, wildcard string, opts domainOptions, stdout io.Writer) error {
	if err := requirePreviewTier("ocel domain use", opts.preview); err != nil {
		return err
	}
	base, err := globalPreviewBaseDomain(wildcard)
	if err != nil {
		return err
	}

	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	return invocation.WithProvider(ctx, cfg, "ocel domain use", commands.OpenOptions{Tier: environmentv1.Tier_TIER_PREVIEW, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		p.Check.End(nil)
		run, provider := p.Run, p.Provider
		req := &contractv1.UsePreviewWildcardRequest{
			Tier:       environmentv1.Tier_TIER_PREVIEW,
			BaseDomain: base,
			Edge:       cfg.EdgeSelection(),
		}
		if _, err := providerprocess.Stream(ctx, provider, "UsePreviewWildcard", req, contractv1connect.ProviderServiceClient.UsePreviewWildcard); err != nil {
			return err
		}
		run.Succeed(fmt.Sprintf("Previews are served on %s", wildcardOf(base)))
		return nil
	})
}

func newUseCommand(invocation commands.Invocation) *cobra.Command {
	var opts domainOptions
	cmd := &cobra.Command{
		Use:   "use <wildcard>",
		Short: "Install (or upgrade) the edge's preview routing and serve every project's previews on this wildcard",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			return runDomainUse(cmd.Context(), invocation, cwd, args[0], opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.preview, "preview", false, "Act on the preview tier (required)")
	return commands.DeclareRunEvents(commands.DeclareMutating(cmd))
}
