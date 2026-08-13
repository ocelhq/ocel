package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/node"
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

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runBootstrap(ctx, defaultDeps(), cwd, bootstrapOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	bootstrapCmd.Flags().BoolVarP(&bootstrapOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
	bootstrapCmd.Flags().BoolVar(&bootstrapOpts.preview, "preview", false, "Stand up the preview infrastructure instead of the production infrastructure")
}

func runBootstrap(ctx context.Context, d deps, cwd string, opts bootstrapOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	if err := node.Ensure(cfg.Dir); err != nil {
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
		proceed, err := confirmBootstrap(ctx, class, provider.Package, stdout, stdin)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(stdout, "Interrupted.")
				return &ExitError{Code: interruptExitCode}
			}
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	ctx, run, err := startRun(ctx, cfg, "ocel bootstrap")
	if err != nil {
		return err
	}
	defer run.Close()

	ui := deployui.New(stdout, run, sessionFormat(), verboseEnabled())
	defer ui.Close()

	provW := ui.BuildWriter()
	err = runProviderSession(ctx, d, cfg, provider, provW, provW, func(runner *providerrunner.Runner) error {
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

func confirmBootstrap(ctx context.Context, class deploymentsv1.Environment_Class, providerPackage string, stdout io.Writer, stdin io.Reader) (bool, error) {
	infra := "production"
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		infra = "preview"
	}
	return confirmYN(ctx, fmt.Sprintf("Bootstrap %s infrastructure with %s?", infra, providerPackage), stdout, stdin)
}
