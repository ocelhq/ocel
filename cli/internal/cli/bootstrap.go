package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/platform"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type bootstrapOptions struct {
	yes     bool
	preview bool
}

var bootstrapOpts bootstrapOptions

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
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.preview, "preview", false, "Stand up the preview infrastructure instead of the production infrastructure")
}

func runBootstrap(ctx context.Context, cwd string, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := platform.Ensure(cfg.Dir); err != nil {
		return err
	}

	provider, err := cfg.RequireProvider()
	if err != nil {
		return err
	}

	class := deploymentsv1.Environment_CLASS_PRODUCTION
	if opts.preview {
		class = deploymentsv1.Environment_CLASS_PREVIEW
	}

	if !opts.yes && isReaderTTY(stdin) {
		proceed, err := confirmBootstrap(class, provider.Package, stdout, stdin)
		if err != nil {
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	ui := deployui.New(stdout, cfg.Dir, "ocel bootstrap", verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
		req := &deploymentsv1.BootstrapRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Class:           class,
		}
		if err := runner.Bootstrap(ctx, req, ui.Event); err != nil {
			return err
		}
		ui.Finish("Bootstrapped")
		return nil
	})
	if err != nil {
		return failSession(ctx, ui, err)
	}
	return nil
}

func confirmBootstrap(class deploymentsv1.Environment_Class, providerPackage string, stdout io.Writer, stdin io.Reader) (bool, error) {
	infra := "production"
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		infra = "preview"
	}
	return confirmYN(fmt.Sprintf("Bootstrap %s infrastructure with %s?", infra, providerPackage), stdout, stdin)
}
