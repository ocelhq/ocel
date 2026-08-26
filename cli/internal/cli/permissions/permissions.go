package permissions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/edgewire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "permissions <bootstrap|deploy>",
		Aliases: []string{"perms"},
		Short:   "Print the permissions bootstrap or deploy credentials need",
		Long: "Print the permissions bootstrap or deploy credentials need.\n\n" +
			"`bootstrap` is what bootstrapping runs under, `deploy` the smaller set deploys and " +
			"previews run under.",
		Example: "  $ ocel permissions deploy",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tier, err := tierArg(args)
			if err != nil {
				_ = cmd.Help()
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()

			return Run(ctx, deps, cwd, tier, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func Run(ctx context.Context, deps cmddeps.Deps, cwd string, tier contractv1.CredentialTier, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stderr, stderr, deps.HostTrust, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		permissions, err := client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{
			Tier: tier,
			Edge: edgewire.Selection(cfg),
		})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return predates(runner.Package())
			}
			return err
		}
		groups := permissions.GetGroups()
		if len(groups) == 0 {
			return predates(runner.Package())
		}
		if len(groups) == 1 {
			fmt.Fprintln(stdout, groups[0].GetDocument())
			return nil
		}
		for i, group := range groups {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			if heading := group.GetHeading(); heading != "" {
				fmt.Fprintln(stdout, heading)
				fmt.Fprintln(stdout)
			}
			fmt.Fprintln(stdout, group.GetDocument())
		}
		return nil
	})
}

func predates(pkg string) error {
	return fmt.Errorf("%s cannot say what permissions these credentials need; it predates them. Upgrade the provider pinned in this project and try again", pkg)
}

func tierArg(args []string) (contractv1.CredentialTier, error) {
	if len(args) == 0 {
		return contractv1.CredentialTier_CREDENTIAL_TIER_UNSPECIFIED,
			errors.New("name the credentials to print, bootstrap or deploy")
	}
	switch args[0] {
	case "bootstrap":
		return contractv1.CredentialTier_CREDENTIAL_TIER_BOOTSTRAP, nil
	case "deploy":
		return contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY, nil
	default:
		return contractv1.CredentialTier_CREDENTIAL_TIER_UNSPECIFIED,
			fmt.Errorf("the credentials to print are bootstrap or deploy, not %q", args[0])
	}
}
