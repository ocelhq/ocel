package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func NewLogoutCommand(dependencies Dependencies) *cobra.Command {
	return commands.DeclareMutating(commands.ReserveStdout(&cobra.Command{
		Use:     "logout",
		Short:   "Log out of the Ocel console",
		Example: "  $ ocel logout",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogout(cmd.Context(), dependencies, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}))
}

func runLogout(ctx context.Context, dependencies Dependencies, stdout, stderr io.Writer) error {
	asJSON := dependencies.Presentation(stdout).Format == terminal.FormatJSON
	creds, err := dependencies.LoadCredentials()
	if err != nil {
		if errors.Is(err, console.ErrNotLoggedIn) {
			if asJSON {
				return terminal.WriteResultJSON(stdout, &resultv1.LogoutResult{})
			}
			fmt.Fprintln(stdout, "Not logged in.")
			return nil
		}
		return fmt.Errorf("could not read stored credentials: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	revokeErr := console.New(console.BaseURL(creds.APIURL)).SignOut(ctx, creds.AccessToken)
	if revokeErr != nil {
		fmt.Fprintf(stderr, "%s Couldn't revoke the session on the console: %v\n", terminal.PaletteFor(stderr).WarnMark(), revokeErr)
	}

	if err := dependencies.DeleteCredentials(); err != nil {
		return fmt.Errorf("failed to clear local credentials: %w", err)
	}
	if asJSON {
		return terminal.WriteResultJSON(stdout, &resultv1.LogoutResult{LoggedOut: true, SessionRevoked: revokeErr == nil})
	}
	fmt.Fprintf(stdout, "%s Logged out\n", terminal.PaletteFor(stdout).PassMark())
	return nil
}
