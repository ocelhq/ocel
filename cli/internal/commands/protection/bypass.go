package protection

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func newBypassCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bypass",
		Short: "Manage the secrets that let a request past protection",
		Long: "Manage the secrets that let a request past this project's protection.\n\n" +
			"A bypass secret is for a machine, such as a CI job or a monitor, that can't enter a password. A " +
			"request that sends it in the x-ocel-bypass header is let through. Production and previews hold " +
			"their own, at most eight each, and a secret of one tier never passes the other. A secret is shown " +
			"once, when it is created; after that it is named by an id.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newCreateCommand(invocation), newListCommand(invocation), newRevokeCommand(invocation))
	return commands.DeclareReadOnly(cmd)
}

func newCreateCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bypass secret, and print it once",
		Long: "Create a bypass secret for this project's production, or its previews with `--preview`, and print it once.\n\n" +
			"Only the secret is printed on stdout, so it can be piped into a CI secret store. Nothing shows it " +
			"again. Send it in the x-ocel-bypass header; `ocel protection bypass revoke` ends it. " +
			"Creating a secret leaves the password and the login sessions it opened alone.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcome, err := applyChange(cmd.Context(), invocation, t, "ocel protection bypass create",
				&contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_CreateBypassSecret{CreateBypassSecret: &contractv1.CreateBypassSecret{}}},
				func(outcome changeOutcome) string {
					return fmt.Sprintf("Created %s bypass secret %s for %s. This is the only time it is shown: save it now", t.name(), outcome.changed.GetBypassSecretId(), outcome.slug)
				})
			secret := outcome.changed.GetBypassSecret()
			if secret == "" {
				return err
			}
			result := &resultv1.BypassSecretResult{
				Slug:   outcome.slug,
				Tier:   outcome.tier,
				Id:     outcome.changed.GetBypassSecretId(),
				Secret: secret,
				Edges:  outcome.edges(),
			}
			return failAfterResult(invocation, cmd.OutOrStdout(), err, printSecret(invocation, cmd.OutOrStdout(), secret, result))
		},
	}
	t.bind(cmd)
	return commands.DeclareResult(commands.DeclareMutating(commands.ReserveStdout(cmd)), &resultv1.BypassSecretResult{})
}

func newListCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the ids of the bypass secrets, without revealing them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBypassList(cmd.Context(), invocation, t, cmd.OutOrStdout())
		},
	}
	t.bind(cmd)
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.BypassSecretListResult{})
}

func newRevokeCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke one of the bypass secrets",
		Long: "Revoke one of this project's production bypass secrets, or its preview ones with `--preview`.\n\n" +
			"`ocel protection bypass ls` lists the ids. The secret stops working, and so does every " +
			"login session opened on the tier, by password or by bypass: visitors enter the " +
			"password again. The tier stays protected by the password, so nothing is asked first.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			outcome, err := applyChange(cmd.Context(), invocation, t, "ocel protection bypass revoke",
				&contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_RevokeBypassSecret{RevokeBypassSecret: &contractv1.RevokeBypassSecret{Id: id}}},
				func(outcome changeOutcome) string {
					return fmt.Sprintf("Revoked %s bypass secret %s of %s and ended its login sessions", t.name(), id, outcome.slug)
				})
			return printPublication(invocation, cmd.OutOrStdout(), outcome, err)
		},
	}
	t.bind(cmd)
	return commands.DeclareResult(commands.DeclareMutating(commands.PrintOnlyUnderJSON(cmd)), &resultv1.PublicationResult{})
}
