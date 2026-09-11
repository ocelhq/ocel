package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/console/auth"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
)

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to the Ocel console",
		Example: "  $ ocel login\n" +
			"  $ ocel login --force\n" +
			"  $ OCEL_CONSOLE_URL=https://console.example.com ocel login",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := deps.Interrupt(cmd.Context(), cmd.ErrOrStderr())
			defer stop()
			return run(ctx, deps, force, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Log in again even if already logged in")
	return cmd
}

var (
	check = color.New(color.FgGreen).Sprint("✓")
	bold  = color.New(color.Bold).SprintFunc()
	link  = color.New(color.FgCyan, color.Underline).SprintFunc()
	faint = color.New(color.Faint).SprintFunc()
)

func run(ctx context.Context, deps cmddeps.Deps, force bool, stdin io.Reader, out io.Writer) error {
	existing, loadErr := deps.LoadCredentials()
	apiURL := strings.TrimRight(console.EffectiveBaseURL(existing.APIURL), "/")

	if loadErr == nil && !force && sameConsole(existing.APIURL, apiURL) {
		fmt.Fprintf(out, "Already logged in as %s at %s. Pass --force to log in again.\n", identity(existing), apiURL)
		return nil
	}

	client := auth.New(apiURL)
	device, err := client.RequestDeviceCode(ctx)
	if err != nil {
		return fmt.Errorf("could not start login at %s (set $%s to use another console): %w", apiURL, console.URLEnvVar, err)
	}

	code := device.UserCode
	if len(code) == 8 {
		code = code[:4] + "-" + code[4:]
	}
	confirmURL := device.VerificationURIComplete
	if confirmURL == "" {
		confirmURL = device.VerificationURI
	}

	fmt.Fprintf(out, "Code     %s\n", bold(code))
	fmt.Fprintf(out, "Confirm  %s\n\n", link(confirmURL))
	if deps.BrowserReachable(stdin) {
		_ = deps.OpenBrowser(confirmURL)
	}
	fmt.Fprintln(out, faint("Waiting for you to confirm the code…"))

	token, err := pollForToken(ctx, client, device)
	if err != nil {
		return err
	}

	creds := credentials.Credentials{
		AccessToken: token.AccessToken,
		APIURL:      apiURL,
		ExpiresAt:   time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
	}
	if session, sessErr := client.GetSession(ctx, token.AccessToken); sessErr == nil && session != nil {
		creds.Email = session.User.Email
	}

	backend, err := credentials.Save(creds)
	if err != nil {
		return fmt.Errorf("logged in, but failed to save credentials: %w", err)
	}

	fmt.Fprintf(out, "%s Logged in as %s\n", check, identity(creds))
	if backend == credentials.BackendFile {
		fmt.Fprintln(out, faint("  No OS keyring, so the token is saved to a file only you can read."))
	}
	return nil
}

func sameConsole(stored, target string) bool {
	stored = strings.TrimRight(strings.TrimSpace(stored), "/")
	return stored == "" || stored == target
}

func identity(creds credentials.Credentials) string {
	if creds.Email != "" {
		return creds.Email
	}
	return "your account"
}

func pollForToken(ctx context.Context, client *auth.Client, device *auth.DeviceCode) (*auth.TokenResult, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("login cancelled")
		case <-time.After(interval):
		}

		token, err := client.PollToken(ctx, device.DeviceCode)
		if err == nil {
			return token, nil
		}

		switch {
		case auth.IsPending(err):
			continue
		case auth.IsSlowDown(err):
			interval += 5 * time.Second
			continue
		case auth.IsAccessDenied(err):
			return nil, errors.New("login request was denied")
		case auth.IsExpired(err):
			return nil, errors.New("the login code expired before it was confirmed — run `ocel login` again")
		default:
			return nil, fmt.Errorf("login failed: %w", err)
		}
	}
}
