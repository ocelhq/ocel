package permissions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func NewCommand(invocation commands.Invocation) *cobra.Command {
	return commands.ReserveStdout(&cobra.Command{
		Use:     "permissions <bootstrap|deploy>",
		Aliases: []string{"perms"},
		Short:   "Print the permissions bootstrap or deploy credentials need",
		Long: "Print the permissions bootstrap or deploy credentials need.\n\n" +
			"`bootstrap` is what bootstrapping runs under, `deploy` the smaller set deploys and " +
			"previews run under.",
		Example: "  $ ocel permissions deploy",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			purpose, err := purposeArg(args)
			if err != nil {
				_ = cmd.Help()
				return err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}

			return Run(cmd.Context(), invocation, cwd, purpose, cmd.OutOrStdout())
		},
	})
}

func Run(ctx context.Context, invocation commands.Invocation, cwd string, purpose contractv1.CredentialPurpose, stdout io.Writer) error {
	cfg, err := invocation.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}
	groups, err := credentialPermissions(ctx, invocation, cfg, purpose)
	if err != nil {
		return err
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
}

func credentialPermissions(ctx context.Context, invocation commands.Invocation, cfg *project.Project, purpose contractv1.CredentialPurpose) (groups []*contractv1.CredentialGroup, err error) {
	if _, err := cfg.RequireProvider(); err != nil {
		return nil, err
	}

	ctx, run, err := invocation.Events.Begin(ctx, "ocel permissions", cfg.Dir)
	if err != nil {
		return nil, err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	prov, err := providerclient.Start(ctx, cfg, check, invocation.Questions, providerclient.PinToLock)
	check.End(err)
	if err != nil {
		return nil, err
	}
	defer prov.Close()

	var permissions *contractv1.CredentialPermissionsResponse
	err = prov.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		permissions, err = client.GetCredentialPermissions(ctx, &contractv1.CredentialPermissionsRequest{
			Purpose: purpose,
			Edge:    cfg.EdgeSelection(),
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return permissions.GetGroups(), nil
}

func purposeArg(args []string) (contractv1.CredentialPurpose, error) {
	if len(args) == 0 {
		return contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_UNSPECIFIED,
			errors.New("name the credentials to print, bootstrap or deploy")
	}
	switch args[0] {
	case "bootstrap":
		return contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_BOOTSTRAP, nil
	case "deploy":
		return contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY, nil
	default:
		return contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_UNSPECIFIED,
			fmt.Errorf("the credentials to print are bootstrap or deploy, not %q", args[0])
	}
}
