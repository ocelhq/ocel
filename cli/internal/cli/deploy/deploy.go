package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/runui"
	"github.com/ocelhq/ocel/cli/internal/cli/style"
	"github.com/ocelhq/ocel/cli/internal/deployresult"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/servicemap"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const noBrowserEnvVar = "OCEL_NO_BROWSER"

func canOpenVarsUI(deps cmddeps.Deps, stdin io.Reader, noUI bool) bool {
	if noUI || os.Getenv(noBrowserEnvVar) != "" {
		return false
	}
	return deps.StdinIsTerminal(stdin)
}

const noUIFlagUsage = "Fail on a missing or invalid variable instead of pausing to open the variables UI"

const prebuiltFlagUsage = "Deploy the existing .ocel/output instead of building first (produce it with ocel build)"

type deployOptions struct {
	yes      bool
	tag      string
	prebuilt bool
	noUI     bool
	dry      bool
}

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	var opts deployOptions

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy this project to your own infrastructure",
		Long: "Deploy this project to your own infrastructure.\n\n" +
			"Builds the apps, provisions the resources they declare, and releases the result " +
			"into your provider account. Every deploy is kept: list them with `ocel deployments`, " +
			"return to one with `ocel rollback`.",
		Example: "  $ ocel deploy\n" +
			"  $ ocel deploy --dry\n" +
			"  $ ocel deploy --tag v1.2.0\n" +
			"  $ ocel deploy --prebuilt\n" +
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

	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip the confirmation prompt")
	cmd.Flags().StringVar(&opts.tag, "tag", "", "Mark this deploy with an immutable `label` to roll back to by name (ocel rollback --tag)")
	cmd.Flags().BoolVar(&opts.prebuilt, "prebuilt", false, prebuiltFlagUsage)
	cmd.Flags().BoolVar(&opts.noUI, "no-ui", false, noUIFlagUsage)
	cmd.Flags().BoolVar(&opts.dry, "dry", false, "Show what deploying would change, and change nothing")

	return cmd
}

func runDeploy(ctx context.Context, deps cmddeps.Deps, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	if err := deployresult.Clear(cfg.Dir); err != nil {
		return err
	}
	if err := servicemap.Clear(cfg.Dir); err != nil {
		return err
	}

	return runui.Run(ctx, deps, cfg, runui.Spec{Command: "ocel deploy", Dry: opts.dry}, stdout, func(ctx context.Context, runner *provider.Runner, ui *runui.Session) error {
		willConfirm := !opts.dry && !opts.yes && deps.StdinIsTerminal(stdin)
		knownSlugs, err := preflightDeploy(ctx, deps, runner, cfg, willConfirm, stdout, stdin)
		if err != nil {
			return err
		}

		if willConfirm {
			proceed, err := confirmDeploy(ctx, cfg.Slug, runner.Package(), knownSlugs, stdout, stdin)
			if err != nil {
				return err
			}
			if !proceed {
				fmt.Fprintln(stdout, "Aborted.")
				return nil
			}
		}

		ui.Building(firstApp(cfg))
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
			ui:      ui,
			stdout:  stdout,
			enabled: canOpenVarsUI(deps, stdin, opts.noUI),
		}
		manifest, err := recovery.buildManifest(ctx, opts.prebuilt)
		if err != nil {
			ui.BuildOK(true)
			return err
		}
		ui.BuildOK(false)
		if manifest == nil {
			ui.Finish("Nothing to deploy")
			return nil
		}

		env := &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PRODUCTION,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED,
		}
		req := &contractv1.DeployRequest{
			Manifest:    manifest,
			Environment: env,
			Tag:         opts.tag,
			Edge:        edgewire.Selection(cfg),
			Dry:         opts.dry,
		}

		var out deployOutcome
		if err := provider.Stream(ctx, runner, "Deploy", req, contractv1connect.ProviderServiceClient.Deploy, out.collect(ui)); err != nil {
			return err
		}
		if opts.dry {
			return nil
		}

		if err := recordDeployResult(cfg, manifest, env, opts.tag, out.promotionID, out.appURLs); err != nil {
			return err
		}
		if err := publishServiceMap(cfg, manifest, env, opts.tag, out.promotionID, out.links); err != nil {
			return err
		}
		ui.Finish("Deployed")
		return nil
	})
}

func firstApp(cfg *projectconfig.Config) string {
	if len(cfg.Apps) == 0 {
		return cfg.Slug
	}
	return cfg.Apps[0].Name
}

func confirmDeploy(ctx context.Context, slug, providerPackage string, knownSlugs []string, stdout io.Writer, stdin io.Reader) (bool, error) {
	if len(knownSlugs) == 0 {
		return confirm(ctx, fmt.Sprintf("Deploy %s with %s?", slug, providerPackage), stdin)
	}
	fmt.Fprintf(stdout, "No existing deployment for slug %q.\nThis will create a NEW project.\nThis backend already has: %s\n",
		slug, strings.Join(knownSlugs, ", "))
	return confirm(ctx, "Continue?", stdin)
}

func confirm(ctx context.Context, title string, stdin io.Reader) (bool, error) {
	var proceed bool
	err := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Affirmative("Yes").
			Negative("No").
			Value(&proceed),
	)).WithTheme(style.Theme).WithInput(stdin).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return proceed, nil
}
