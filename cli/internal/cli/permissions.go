package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func newPermissionsCommand(deps cmddeps.Deps) *cobra.Command {
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
			tier, err := credentialTierArg(args)
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

			return RunPermissions(ctx, deps, cwd, tier, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func RunPermissions(ctx context.Context, deps cmddeps.Deps, cwd string, tier contractv1.CredentialTier, stdout, stderr io.Writer) error {
	cfg, err := projectconfig.Resolve(ctx, cwd, deps.ConfigPath())
	if err != nil {
		return err
	}

	return provider.Drive(ctx, cfg, stderr, stderr, func(runner *provider.Runner) error {
		client, err := runner.Client()
		if err != nil {
			return err
		}
		permissions, err := client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{Tier: tier})
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				return fmt.Errorf("%s cannot say what permissions these credentials need; it predates them. Upgrade the provider pinned in this project and try again", runner.Package())
			}
			return err
		}
		fmt.Fprintln(stdout, permissions.GetDocument())
		return nil
	})
}

func credentialTierArg(args []string) (contractv1.CredentialTier, error) {
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
