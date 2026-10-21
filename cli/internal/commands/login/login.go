package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/terminal"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

type Dependencies struct {
	commands.Invocation
	LoadCredentials   func() (console.Credentials, error)
	SaveCredentials   func(console.Credentials) (console.CredentialStore, error)
	DeleteCredentials func() error
	OpenBrowser       func(url string) error
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in to the Ocel console",
		Example: "  $ ocel login\n" +
			"  $ ocel login --force\n" +
			"  $ OCEL_CONSOLE_URL=https://console.example.com ocel login",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), dependencies, force, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Log in again even if already logged in")
	return commands.DeclareResult(commands.DeclareMutating(commands.ReserveStdout(cmd)), &resultv1.LoginResult{})
}

func run(ctx context.Context, dependencies Dependencies, force bool, stdin io.Reader, out, stderr io.Writer) error {
	asJSON := dependencies.Presentation(out).Format == terminal.FormatJSON
	prompts := out
	if asJSON {
		prompts = stderr
	}

	existing, loadErr := dependencies.LoadCredentials()
	apiURL := console.BaseURL(existing.APIURL)

	if loadErr == nil && !force && sameConsole(existing.APIURL, apiURL) {
		offer, err := console.NewCloudOffers(dependencies.LoadCredentials, dependencies.SaveCredentials).Resolve(ctx, existing)
		if asJSON {
			return terminal.WriteResultJSON(out, loginResult(existing.Email, apiURL, offer, err))
		}
		fmt.Fprintf(out, "Already logged in as %s at %s. Pass --force to log in again.\n", identity(existing), apiURL)
		printCloudOffer(out, apiURL, offer, err)
		return nil
	}

	client := console.New(apiURL)
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

	p := terminal.PaletteFor(prompts)
	fmt.Fprintf(prompts, "Code     %s\n", p.Bold(code))
	fmt.Fprintf(prompts, "Confirm  %s\n\n", p.Link(confirmURL))
	if dependencies.IsBrowserReachable(stdin) {
		_ = dependencies.OpenBrowser(confirmURL)
	}
	fmt.Fprintln(prompts, p.Faint("Waiting for you to confirm the code…"))

	token, err := pollForToken(ctx, client, device)
	if err != nil {
		return err
	}

	creds := console.Credentials{
		AccessToken: token.AccessToken,
		APIURL:      apiURL,
		ExpiresAt:   time.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
	}
	if session, sessErr := client.GetSession(ctx, token.AccessToken); sessErr == nil && session != nil {
		creds.Email = session.User.Email
	}
	offer, offerErr := client.ReadCloudOffer(ctx)
	if offerErr == nil {
		creds.Cloud = offer
		creds.CloudFetchedAt = time.Now()
	}

	store, err := dependencies.SaveCredentials(creds)
	if err != nil {
		return fmt.Errorf("logged in, but failed to save credentials: %w", err)
	}

	if asJSON {
		return terminal.WriteResultJSON(out, loginResult(creds.Email, apiURL, offer, offerErr))
	}
	fmt.Fprintf(out, "%s Logged in as %s\n", p.PassMark(), identity(creds))
	if store == console.FileStore {
		fmt.Fprintln(out, p.Faint("  No OS keyring, so the token is saved to a file only you can read."))
	}
	printCloudOffer(out, apiURL, offer, offerErr)
	return nil
}

func loginResult(email, apiURL string, offer *console.CloudOffer, offerErr error) *resultv1.LoginResult {
	result := &resultv1.LoginResult{Email: email, ConsoleUrl: apiURL}
	switch {
	case offerErr != nil:
		result.CloudError = offerErr.Error()
	case offer != nil:
		result.Cloud = &consolev1.CloudOffer{ApiUrl: offer.APIURL, ProviderUrl: offer.ProviderURL, Audience: offer.Audience}
	}
	return result
}

func printCloudOffer(out io.Writer, apiURL string, offer *console.CloudOffer, err error) {
	p := terminal.PaletteFor(out)
	switch {
	case err != nil:
		fmt.Fprintln(out, p.Faint(fmt.Sprintf("  Could not learn whether %s offers Ocel Cloud: %v", apiURL, err)))
	case offer != nil:
		fmt.Fprintln(out, p.Faint(fmt.Sprintf("  %s offers Ocel Cloud at %s.", apiURL, offer.APIURL)))
	default:
		fmt.Fprintln(out, p.Faint(fmt.Sprintf("  %s offers no Ocel Cloud.", apiURL)))
	}
}

func sameConsole(stored, target string) bool {
	stored = strings.TrimRight(strings.TrimSpace(stored), "/")
	return stored == "" || stored == target
}

func identity(creds console.Credentials) string {
	if creds.Email != "" {
		return creds.Email
	}
	return "your account"
}

func pollForToken(ctx context.Context, client *console.Client, device *console.DeviceCode) (*console.Token, error) {
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
		case console.IsAuthorizationPending(err):
			continue
		case console.IsSlowDown(err):
			interval += 5 * time.Second
			continue
		case console.IsAccessDenied(err):
			return nil, errors.New("login request was denied")
		case console.IsDeviceCodeExpired(err):
			return nil, errors.New("the login code expired before it was confirmed — run `ocel login` again")
		default:
			return nil, fmt.Errorf("login failed: %w", err)
		}
	}
}
