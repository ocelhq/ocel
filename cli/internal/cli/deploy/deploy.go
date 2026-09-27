package deploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	"github.com/ocelhq/ocel/pkg/constants"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const prebuiltFlagUsage = "Deploy the existing " + constants.ProjectStateDirName + "/output instead of building first (produce it with ocel build)"

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

func runDeploy(ctx context.Context, deps cmddeps.Deps, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
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

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	gate := deps.Gate(consent.Convergent, "ocel deploy", opts.yes, stdout, stdin)
	gate.Dry = opts.dry
	if err := gate.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel deploy", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.HostTrust, providerclient.ChoosePinning(opts.dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	facts, err := preflightDeploy(ctx, deps, gate, check, prov, cfg, stdout, stdin)
	check.End(err)
	if err != nil {
		return err
	}
	if facts.declined {
		run.Finish("Nothing deployed")
		return nil
	}

	browser := deps.BrowserReachable(stdin)
	scope := envwire.Scope(cfg, false, "")
	scope.Browser = browser
	recovery := gateRecovery{
		deps: deps,
		cfg:  cfg,
		prov: prov,
		newGate: func(synced envgate.EnvSource) *envgate.Gate {
			scope := scope
			scope.EnvSource = synced
			return envgate.New(envwire.Values{
				Provider: prov,
				Slug:     cfg.Slug,
				Tier:     environmentv1.Tier_TIER_PRODUCTION,
			}, scope)
		},
		command:        "ocel deploy",
		compute:        facts.compute,
		containerArchs: facts.containerArchs,
		urls:           facts.urls,
		dry:            opts.dry,
		enabled:        !opts.dry && browser,
	}
	build := run.Phase(progressv1.Phase_PHASE_BUILD)
	manifest, inline, err := recovery.buildManifest(ctx, build, opts.prebuilt)
	build.End(err)
	if err != nil {
		return err
	}
	if manifest == nil {
		run.Finish("Nothing to deploy")
		return nil
	}

	env := &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED,
	}
	registry, err := imageRegistry(ctx, prov, cfg, env.GetTier())
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
		return showDeployPlan(ctx, run, prov, req, "Proposed changes to production")
	}

	out, err := streamDeploy(ctx, prov, cfg.Slug, req, inline)
	if err != nil {
		return err
	}

	if err := recordDeployResult(cfg, manifest, env, opts.tag, out.promotionID, out.apps); err != nil {
		return err
	}
	if err := publishServiceMap(cfg, manifest, env, opts.tag, out.promotionID, out.bindings); err != nil {
		return err
	}
	run.Deployed("Deployed", out.urlNotes, out.flip)
	return nil
}
