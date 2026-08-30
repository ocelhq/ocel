package deploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const noBrowserEnvVar = "OCEL_NO_BROWSER"

func canOpenVarsUI(deps cmddeps.Deps, stdin io.Reader) bool {
	if os.Getenv(noBrowserEnvVar) != "" {
		return false
	}
	return deps.StdinIsTerminal(stdin)
}

const prebuiltFlagUsage = "Deploy the existing .ocel/output instead of building first (produce it with ocel build)"

type deployOptions struct {
	yes      bool
	dry      bool
	tag      string
	prebuilt bool
}

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	var opts deployOptions

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy this project to your own infrastructure",
		Long: "Deploy this project to your own infrastructure.\n\n" +
			"Builds the apps, provisions the resources they declare, and releases the result " +
			"into your provider account. Every deploy is kept: list them with `ocel deployments`, " +
			"return to one with `ocel rollback`.\n\n" +
			"--dry builds, then prints every change the deploy would make to your account and stops.",
		Example: "  $ ocel deploy\n" +
			"  $ ocel deploy --tag v1.2.0\n" +
			"  $ ocel deploy --prebuilt\n" +
			"  $ ocel deploy --dry\n" +
			"  $ ocel deploy --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			opts := opts
			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return runDeploy(ctx, deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	cmddeps.Yes(cmd, &opts.yes)
	cmd.Flags().StringVar(&opts.tag, "tag", "", "Mark this deploy with an immutable `label` to roll back to by name (ocel rollback --tag)")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	cmd.Flags().BoolVar(&opts.dry, "dry", false, dryFlagUsage)

	return cmd
}

func runDeploy(ctx context.Context, deps cmddeps.Deps, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	if !opts.dry {
		if err := deployresult.Clear(cfg.Dir); err != nil {
			return err
		}
		if err := servicemap.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	spec := deps.Spec(runui.Convergent, "ocel deploy", cfg, opts.yes, stdout, stdin)
	spec.Dry = opts.dry

	return runui.Run(ctx, spec, func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		knownSlugs, compute, err := preflightDeploy(ctx, deps, ui, runner, cfg, stdout, stdin)
		if err != nil {
			return err
		}

		proceed, err := guardNewProject(ctx, ui, cfg, knownSlugs)
		if err != nil || !proceed {
			return err
		}

		ui.Building()
		recovery := gateRecovery{
			deps:   deps,
			cfg:    cfg,
			runner: runner,
			newGate: func() *envgate.Gate {
				return envgate.New(envwire.Values{
					Runner: runner,
					Slug:   cfg.Slug,
					Tier:   environmentv1.Tier_TIER_PRODUCTION,
				}, envwire.Scope(cfg, false, ""))
			},
			compute: compute,
			ui:      ui,
			enabled: !opts.dry && canOpenVarsUI(deps, stdin),
		}
		manifest, err := recovery.buildManifest(ctx, opts.prebuilt)
		if err != nil {
			return err
		}
		if manifest == nil {
			ui.Finish("Nothing to deploy")
			return nil
		}
		ui.BuildOK()

		env := &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PRODUCTION,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED,
		}
		registry, err := imageRegistry(ctx, runner, cfg)
		if err != nil {
			return err
		}

		req := &contractv1.DeployRequest{
			Manifest:    manifest,
			Environment: env,
			Tag:         opts.tag,
			Edge:        edgewire.Selection(cfg),
			Dry:         opts.dry,

			ImageRegistry: registry,
		}

		if opts.dry {
			return showDeployPlan(ctx, runner, ui, req, "Proposed changes to production")
		}

		var out deployOutcome
		if err := provider.Stream(ctx, runner, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy, out.collect(ui)); err != nil {
			return err
		}

		if err := recordDeployResult(cfg, manifest, env, opts.tag, out.promotionID, out.appURLs); err != nil {
			return err
		}
		if err := publishServiceMap(cfg, manifest, env, opts.tag, out.promotionID, out.links); err != nil {
			return err
		}
		ui.Deployed("Deployed", out.appURLs, out.urlNote, out.flip, out.links, out.functions)
		return nil
	})
}
