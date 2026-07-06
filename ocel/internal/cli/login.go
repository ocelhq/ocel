package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/internal/authclient"
	"github.com/ocelhq/ocel/internal/credentials"
)

// defaultAPIURL is used when neither --api-url nor OCEL_API_URL is set.
// There is no deployed production Ocel server yet, so this points at the
// local dev server started by `pnpm --filter web dev`.
const defaultAPIURL = "http://localhost:3000"

var loginAPIURL string
var loginForce bool

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate the Ocel CLI with your account",
	Long: "Authenticate the Ocel CLI with your account using your browser.\n\n" +
		"This uses the OAuth 2.0 device authorization flow: the CLI requests a\n" +
		"one-time code, you approve it on the Ocel dashboard, and the CLI\n" +
		"stores the resulting access token securely on this machine.",
	RunE: runLogin,
}

func init() {
	loginCmd.Flags().StringVar(&loginAPIURL, "api-url", resolveAPIURL(), "Base URL of the Ocel server")
	loginCmd.Flags().BoolVar(&loginForce, "force", false, "Re-authenticate even if already logged in")
}

// resolveAPIURL determines the default --api-url value: the OCEL_API_URL
// env var if set, otherwise defaultAPIURL.
func resolveAPIURL() string {
	if v := strings.TrimSpace(os.Getenv("OCEL_API_URL")); v != "" {
		return v
	}
	return defaultAPIURL
}

func runLogin(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	apiURL := strings.TrimRight(loginAPIURL, "/")

	if !loginForce {
		if existing, err := credentials.Load(); err == nil {
			identity := existing.Email
			if identity == "" {
				identity = "your account"
			}
			fmt.Fprintf(out, "Already logged in as %s (%s).\n", identity, existing.APIURL)
			fmt.Fprintln(out, "Run `ocel logout` first, or pass --force to log in again.")
			return nil
		}
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := authclient.New(apiURL)

	device, err := client.RequestDeviceCode(ctx)
	if err != nil {
		return fmt.Errorf("could not start login: %w", describeConnError(err, apiURL))
	}

	displayCode := device.UserCode
	if len(displayCode) == 8 {
		displayCode = displayCode[:4] + "-" + displayCode[4:]
	}

	openURL := device.VerificationURIComplete
	if openURL == "" {
		openURL = device.VerificationURI
	}

	fmt.Fprintln(out, "First copy your one-time code:")
	fmt.Fprintf(out, "\n  %s\n\n", displayCode)
	fmt.Fprintln(out, "Then visit this URL to confirm:")
	fmt.Fprintf(out, "\n  %s\n\n", openURL)

	if err := browser.OpenURL(openURL); err != nil {
		fmt.Fprintln(out, "Couldn't open your browser automatically — open the link above manually.")
	} else {
		fmt.Fprintln(out, "Opened your browser. Waiting for you to confirm the code…")
	}

	token, err := pollForToken(ctx, client, device)
	if err != nil {
		return err
	}

	expiresAt := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)

	creds := credentials.Credentials{
		AccessToken: token.AccessToken,
		APIURL:      apiURL,
		ExpiresAt:   expiresAt,
	}

	// Best-effort: fetch the signed-in user's email for a friendlier
	// confirmation message. Not fatal if it fails.
	if session, sessErr := client.GetSession(ctx, token.AccessToken); sessErr == nil && session != nil {
		creds.Email = session.User.Email
	}

	backend, err := credentials.Save(creds)
	if err != nil {
		return fmt.Errorf("logged in, but failed to save credentials: %w", err)
	}

	fmt.Fprintln(out)
	if creds.Email != "" {
		fmt.Fprintf(out, "✓ Logged in as %s.\n", creds.Email)
	} else {
		fmt.Fprintln(out, "✓ Logged in.")
	}
	if backend == credentials.BackendFile {
		fmt.Fprintln(out, "  (No OS keyring available — credentials were saved to a local file with restricted permissions.)")
	}

	return nil
}

// pollForToken polls the device/token endpoint until the user approves or
// denies the request, the device code expires, or ctx is cancelled (e.g.
// Ctrl+C).
func pollForToken(ctx context.Context, client *authclient.Client, device *authclient.DeviceCode) (*authclient.TokenResult, error) {
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
		case authclient.IsPending(err):
			continue
		case authclient.IsSlowDown(err):
			interval += 5 * time.Second
			continue
		case authclient.IsAccessDenied(err):
			return nil, errors.New("login request was denied")
		case authclient.IsExpired(err):
			return nil, errors.New("the login code expired before it was confirmed — run `ocel login` again")
		default:
			return nil, fmt.Errorf("login failed: %w", err)
		}
	}
}

// describeConnError adds a hint about --api-url when the error looks like a
// connectivity problem, without swallowing the underlying error.
func describeConnError(err error, apiURL string) error {
	return fmt.Errorf("%w (target: %s, override with --api-url)", err, apiURL)
}
