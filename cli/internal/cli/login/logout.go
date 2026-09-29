package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
)

func NewLogoutCommand(deps cmddeps.Deps) *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		Short:   "Log out of the Ocel console",
		Example: "  $ ocel logout",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogout(cmd.Context(), deps, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func runLogout(ctx context.Context, deps cmddeps.Deps, stdout, stderr io.Writer) error {
	creds, err := deps.LoadCredentials()
	if err != nil {
		if errors.Is(err, console.ErrNotLoggedIn) {
			fmt.Fprintln(stdout, "Not logged in.")
			return nil
		}
		return fmt.Errorf("could not read stored credentials: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := console.New(console.BaseURL(creds.APIURL)).SignOut(ctx, creds.AccessToken); err != nil {
		fmt.Fprintf(stderr, "%s Couldn't revoke the session on the console: %v\n", terminal.PaletteFor(stderr).WarnMark(), err)
	}

	if err := deps.DeleteCredentials(); err != nil {
		return fmt.Errorf("failed to clear local credentials: %w", err)
	}
	fmt.Fprintf(stdout, "%s Logged out\n", terminal.PaletteFor(stdout).PassMark())
	return nil
}
