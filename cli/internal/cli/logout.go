package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/authclient"
	"github.com/ocelhq/ocel/cli/internal/credentials"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of the Ocel CLI",
	Long: "Clears the Ocel CLI's stored credentials on this machine and\n" +
		"revokes the session on the server on a best-effort basis.",
	RunE: runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()

	creds, err := credentials.Load()
	if err != nil {
		if errors.Is(err, credentials.ErrNotLoggedIn) {
			fmt.Fprintln(out, "You are not logged in.")
			return nil
		}
		return fmt.Errorf("could not read stored credentials: %w", err)
	}

	// Best-effort server-side revocation. Local credentials are cleared
	// regardless of whether this succeeds, so a stale/unreachable server
	// never prevents the user from logging out locally. An explicit
	// --api-url overrides the persisted credentials' API URL.
	if apiURL := effectiveAPIURL(cmd, creds.APIURL); apiURL != "" {
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		client := authclient.New(apiURL)
		if err := client.SignOut(ctx, creds.AccessToken); err != nil {
			fmt.Fprintf(out, "Warning: could not revoke the session on the server (%v). Continuing with local logout.\n", err)
		}
	}

	if err := credentials.Delete(); err != nil {
		return fmt.Errorf("failed to clear local credentials: %w", err)
	}

	fmt.Fprintln(out, "✓ Logged out.")
	return nil
}
