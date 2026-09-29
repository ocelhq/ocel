package deploy

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/deployrecord"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/valuestore"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const prebuiltFlagUsage = "Deploy the existing " + statedir.Name + "/output instead of building first (produce it with ocel build)"

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
			return runDeploy(cmd.Context(), deps, cwd, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}

	cmddeps.Yes(cmd, &opts.yes)
	cmd.Flags().StringVar(&opts.tag, "tag", "", "Mark this deploy with an immutable `label` to roll back to by name (ocel rollback --tag)")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	cmd.Flags().BoolVar(&opts.dry, "dry", false, dryFlagUsage)

	return cmd
}

func runDeploy(ctx context.Context, deps cmddeps.Deps, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	cfg, err := deps.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	if !opts.dry {
		if err := deployrecord.Clear(cfg.Dir); err != nil {
			return err
		}
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}
	policy := deps.ConsentPolicy("ocel deploy", opts.yes, stdout, stdin)
	policy.DryRun = opts.dry
	if err := policy.Refuse(); err != nil {
		return err
	}

	ctx, run, err := deps.Events.Begin(ctx, "ocel deploy", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, deps.Questions, providerclient.ChoosePinning(opts.dry))
	if err != nil {
		return err
	}
	defer prov.Close()

	facts, err := preflightDeploy(ctx, deps, policy, check, prov, cfg, opts.prebuilt, stdout, stdin)
	check.End(err)
	if err != nil {
		return err
	}
	if facts.declined {
		run.Succeed("Nothing deployed to production")
		return nil
	}
	cfg = facts.project

	browser := deps.BrowserReachable(stdin)
	scope := variablescope.Of(cfg, environmentv1.Tier_TIER_PRODUCTION, "")
	scope.Browser = browser
	recovery := variablesRecovery{
		deps: deps,
		cfg:  cfg,
		prov: prov,
		tier: environmentv1.Tier_TIER_PRODUCTION,
		newDeclarations: func(synced variables.EnvSource) *variables.Declarations {
			scope := scope
			scope.EnvSource = synced
			return variables.NewDeclarations(valuestore.Store{
				Provider: prov,
				Project:  cfg,
				Tier:     environmentv1.Tier_TIER_PRODUCTION,
			}, scope)
		},
		command:        "ocel deploy",
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
		run.Succeed(nothingToDeploy(cfg))
		return nil
	}

	env := &environmentv1.Environment{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED,
	}
	registry, err := projectRegistry(cfg)
	if err != nil {
		return err
	}

	req := &contractv1.DeployRequest{
		Manifest:    manifest,
		Environment: env,
		Tag:         opts.tag,
		Edge:        cfg.EdgeSelection(),
		Dry:         opts.dry,

		ProjectRegistry: registry,
		InlineBindings:  inline,
	}

	if opts.dry {
		return showDeployPlan(ctx, run, prov, req, "Proposed changes to production", cfg.Slug, "production")
	}

	out, err := streamDeploy(ctx, prov, req)
	if err != nil {
		return err
	}

	record, err := deployrecord.New(cfg, manifest, env, opts.tag, out.promotionID, out.apps)
	if err != nil {
		return err
	}
	if err := deployrecord.Write(cfg.Dir, record); err != nil {
		return err
	}
	run.Succeed(fmt.Sprintf("Deployed %s to production", cfg.Slug))
	return nil
}
