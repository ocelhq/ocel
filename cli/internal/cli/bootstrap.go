package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// bootstrapOptions holds the flags accepted by `ocel bootstrap`.
type bootstrapOptions struct {
	yes bool
}

var bootstrapOpts bootstrapOptions

// bootstrapCmd creates the account-global resources the configured provider
// needs before deploys can run. It does nothing itself: it delegates entirely
// to the provider's Bootstrap RPC. For the AWS provider this is a
// one-time-per-account action (re-run only when the provider's bootstrap
// requirements change).
var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Provision the account-global resources your provider needs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return runBootstrap(ctx, cwd, bootstrapOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	bootstrapCmd.Flags().BoolVarP(&bootstrapOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
}

// runBootstrap resolves the project config, verifies auth, requires a
// configured provider, confirms with the user, then spawns the provider and
// drives its Bootstrap RPC to a terminal result. Bootstrap sends no manifest:
// the provider decides what account-global resources to create.
func runBootstrap(ctx context.Context, cwd string, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	if _, err := loadCredentials(); err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	if !opts.yes && isReaderTTY(stdin) {
		proceed, err := confirmBootstrap(provider.Package, stdout, stdin)
		if err != nil {
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	return runProviderSession(ctx, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		req := &providerv1.BootstrapRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
		}
		if err := runner.Bootstrap(ctx, req, func(ev *providerv1.DeployEvent) { streamDeployEvent(stdout, ev) }); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "✓ Bootstrap succeeded.")
		return nil
	})
}

// confirmBootstrap prints the "Bootstrap account-global resources with
// <provider>? [y/N]" prompt and returns the user's yes/no answer (see
// confirmYN in deploy.go).
func confirmBootstrap(providerPackage string, stdout io.Writer, stdin io.Reader) (bool, error) {
	return confirmYN(fmt.Sprintf("Bootstrap account-global resources with %s?", providerPackage), stdout, stdin)
}
