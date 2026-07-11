package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/previewid"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// currentGitBranch resolves the current git branch of dir. A package var so
// tests can stub it without a real repo, mirroring loadCredentials.
var currentGitBranch = func(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("determine current git branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("determine current git branch: empty ref")
	}
	return branch, nil
}

// discoverPRNumber reads the pull request number from a well-known CI
// environment variable, returning "" when unset. A package var so tests can
// stub it.
var discoverPRNumber = func() string {
	return os.Getenv("OCEL_PR_NUMBER")
}

// previewUpOptions holds the flags accepted by `ocel preview` / `ocel preview up`.
type previewUpOptions struct {
	name string
}

// previewRmOptions holds the flags accepted by `ocel preview rm`.
type previewRmOptions struct {
	ref  string
	name string
	yes  bool
}

var (
	previewUpOpts previewUpOptions
	previewRmOpts previewRmOptions
)

// previewCmd stands up, tears down, and lists preview environments. Bare
// `ocel preview` is an alias for `ocel preview up`.
var previewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Stand up a preview environment for the current branch",
	Args:  cobra.NoArgs,
	RunE:  runPreviewUpCmd,
}

var previewUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Stand up (or update) a preview environment",
	Args:  cobra.NoArgs,
	RunE:  runPreviewUpCmd,
}

var previewRmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Tear down a preview environment",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runPreviewRm(ctx, cwd, previewRmOpts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

var previewLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List preview environments",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runPreviewLs(ctx, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func runPreviewUpCmd(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runPreviewUp(ctx, cwd, previewUpOpts, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func init() {
	previewUpCmd.Flags().StringVar(&previewUpOpts.name, "name", "", "Name a persistent (staging-like) preview instead of the branch's ephemeral one")
	previewCmd.Flags().StringVar(&previewUpOpts.name, "name", "", "Name a persistent (staging-like) preview instead of the branch's ephemeral one")

	previewRmCmd.Flags().StringVar(&previewRmOpts.ref, "ref", "", "Tear down the ephemeral preview for an explicit git ref")
	previewRmCmd.Flags().StringVar(&previewRmOpts.name, "name", "", "Tear down the named persistent preview")
	previewRmCmd.Flags().BoolVarP(&previewRmOpts.yes, "yes", "y", false, "Skip the confirmation prompt")

	previewCmd.AddCommand(previewUpCmd)
	previewCmd.AddCommand(previewRmCmd)
	previewCmd.AddCommand(previewLsCmd)
}

// runPreviewUp resolves the target Environment, preflights the preview
// infrastructure (refusing before provisioning when it is missing or the wrong
// class), then drives the provider's Deploy RPC and prints the connection
// outputs on success. `ocel preview` and `ocel deploy` share the Deploy RPC and
// diverge only by the Environment sent.
func runPreviewUp(ctx context.Context, cwd string, opts previewUpOptions, stdout, stderr io.Writer) error {
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

	env, err := resolveUpEnvironment(cwd, opts.name)
	if err != nil {
		return err
	}

	manifest, err := collectAndBuildManifest(ctx, cfg, stdout, stderr)
	if err != nil {
		return err
	}

	return runProviderSession(ctx, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightPreview(ctx, runner, provider); err != nil {
			return err
		}

		req := &providerv1.DeployRequest{
			Manifest:        manifest,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			Environment:     env,
		}

		var stackOutputs []*providerv1.ResourceOutput
		onEvent := func(ev *providerv1.DeployEvent) {
			streamDeployEvent(stdout, ev)
			if res := ev.GetResult(); res != nil {
				stackOutputs = res.GetOutputs()
			}
		}
		if err := runner.Deploy(ctx, req, onEvent); err != nil {
			return err
		}

		fmt.Fprintf(stdout, "✓ Preview %s is up.\n", env.GetIdentity())
		printResourceOutputs(stdout, stackOutputs)
		return nil
	})
}

// runPreviewRm resolves the addressing Environment, guards it against the
// preview infrastructure, prompts before destroying a persistent preview, and
// drives the provider's Destroy RPC.
func runPreviewRm(ctx context.Context, cwd string, opts previewRmOptions, stdout, stderr io.Writer, stdin io.Reader) error {
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

	env, err := resolveRmEnvironment(cwd, opts)
	if err != nil {
		return err
	}

	persistent := env.GetLifecycle() == providerv1.Environment_LIFECYCLE_PERSISTENT
	if persistent && !opts.yes && isReaderTTY(stdin) {
		proceed, err := confirmDestroyPreview(env.GetIdentity(), stdout, stdin)
		if err != nil {
			return err
		}
		if !proceed {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	return runProviderSession(ctx, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		if err := preflightPreview(ctx, runner, provider); err != nil {
			return err
		}

		req := &providerv1.DestroyRequest{
			Environment:     env,
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
		}
		if err := runner.Destroy(ctx, req, func(ev *providerv1.DeployEvent) { streamDeployEvent(stdout, ev) }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "✓ Preview %s torn down.\n", env.GetIdentity())
		return nil
	})
}

// runPreviewLs drives the provider's ListEnvironments RPC and renders each
// preview environment.
func runPreviewLs(ctx context.Context, cwd string, stdout, stderr io.Writer) error {
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

	return runProviderSession(ctx, cfg, provider, stdout, stderr, func(runner *providerrunner.Runner) error {
		resp, err := runner.ListEnvironments(ctx, &providerv1.ListEnvironmentsRequest{
			Options:         []byte(provider.Options),
			ProtocolVersion: manifestbuilder.SchemaVersion,
			ProjectId:       cfg.ProjectID,
		})
		if err != nil {
			return err
		}
		renderEnvironments(stdout, resp.GetEnvironments())
		return nil
	})
}

// resolveUpEnvironment builds the Environment `ocel preview up` provisions: a
// named preview is persistent and declared; an unnamed one is ephemeral, keyed
// off the current git branch with the PR number carried as a display label.
func resolveUpEnvironment(cwd, name string) (*providerv1.Environment, error) {
	if name != "" {
		return &providerv1.Environment{
			Class:          providerv1.Environment_CLASS_PREVIEW,
			Lifecycle:      providerv1.Environment_LIFECYCLE_PERSISTENT,
			Identity:       name,
			IdentitySource: providerv1.Environment_IDENTITY_SOURCE_DECLARED,
		}, nil
	}

	branch, err := currentGitBranch(cwd)
	if err != nil {
		return nil, err
	}
	id, err := previewid.Resolve(branch, discoverPRNumber())
	if err != nil {
		return nil, err
	}
	return &providerv1.Environment{
		Class:          providerv1.Environment_CLASS_PREVIEW,
		Lifecycle:      providerv1.Environment_LIFECYCLE_EPHEMERAL,
		Identity:       id.Key,
		IdentitySource: providerv1.Environment_IDENTITY_SOURCE_GIT,
		Label:          id.Label,
	}, nil
}

// resolveRmEnvironment builds the addressing Environment for `ocel preview rm`:
// --name targets a persistent preview; --ref targets an explicit ref's
// ephemeral preview; bare targets the current branch's ephemeral preview.
func resolveRmEnvironment(cwd string, opts previewRmOptions) (*providerv1.Environment, error) {
	if opts.name != "" && opts.ref != "" {
		return nil, fmt.Errorf("--name and --ref are mutually exclusive; use one to address a persistent or ephemeral preview")
	}
	if opts.name != "" {
		return &providerv1.Environment{
			Class:          providerv1.Environment_CLASS_PREVIEW,
			Lifecycle:      providerv1.Environment_LIFECYCLE_PERSISTENT,
			Identity:       opts.name,
			IdentitySource: providerv1.Environment_IDENTITY_SOURCE_DECLARED,
		}, nil
	}

	ref := opts.ref
	if ref == "" {
		branch, err := currentGitBranch(cwd)
		if err != nil {
			return nil, err
		}
		ref = branch
	}
	id, err := previewid.Resolve(ref, "")
	if err != nil {
		return nil, err
	}
	return &providerv1.Environment{
		Class:          providerv1.Environment_CLASS_PREVIEW,
		Lifecycle:      providerv1.Environment_LIFECYCLE_EPHEMERAL,
		Identity:       id.Key,
		IdentitySource: providerv1.Environment_IDENTITY_SOURCE_GIT,
	}, nil
}

// confirmDestroyPreview prints the "Destroy persistent preview <name>? [y/N]"
// prompt and returns the user's yes/no answer (see confirmYN in deploy.go).
// Only persistent previews prompt; ephemeral teardown never does.
func confirmDestroyPreview(name string, stdout io.Writer, stdin io.Reader) (bool, error) {
	return confirmYN(fmt.Sprintf("Destroy persistent preview %q?", name), stdout, stdin)
}

// preflightPreview refuses a preview command locally — before anything is
// provisioned — when the preview infrastructure is missing or is the wrong
// class.
func preflightPreview(ctx context.Context, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor) error {
	return preflightClass(ctx, runner, provider, providerv1.Environment_CLASS_PREVIEW, "ocel bootstrap --preview")
}

// preflightClass asks the provider what its ambient account points at and
// refuses locally — before anything is provisioned — when no infrastructure is
// present (directing the user to bootstrapHint) or it is the wrong class for
// the running command. The provider enforces the same class match
// authoritatively; this is the fast local refuse `ocel deploy` and `ocel
// preview` share.
func preflightClass(ctx context.Context, runner *providerrunner.Runner, provider *projectconfig.ProviderDescriptor, required providerv1.Environment_Class, bootstrapHint string) error {
	resp, err := runner.Preflight(ctx, &providerv1.PreflightRequest{
		Options:         []byte(provider.Options),
		ProtocolVersion: manifestbuilder.SchemaVersion,
		RequiredClass:   required,
	})
	if err != nil {
		return err
	}
	if !resp.GetInfrastructurePresent() {
		return fmt.Errorf("no infrastructure is set up yet; run `%s` to create it", bootstrapHint)
	}
	return providerv1.CheckClass(resp.GetInfraClass(), required)
}

// renderEnvironments prints one line per preview environment: identity,
// lifecycle tag, PR label, and age/expiry.
func renderEnvironments(stdout io.Writer, envs []*providerv1.PreviewEnvironment) {
	if len(envs) == 0 {
		fmt.Fprintln(stdout, "No preview environments.")
		return
	}
	for _, e := range envs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\tcreated %s\texpires %s\n",
			e.GetIdentity(),
			lifecycleTag(e.GetLifecycle()),
			labelOrDash(e.GetLabel()),
			epochOrDash(e.GetCreatedAt()),
			epochOrDash(e.GetExpiresAt()),
		)
	}
}

func lifecycleTag(l providerv1.Environment_Lifecycle) string {
	switch l {
	case providerv1.Environment_LIFECYCLE_EPHEMERAL:
		return "ephemeral"
	case providerv1.Environment_LIFECYCLE_PERSISTENT:
		return "persistent"
	default:
		return "unknown"
	}
}

func labelOrDash(label string) string {
	if label == "" {
		return "—"
	}
	return label
}

// epochOrDash renders an epoch-seconds timestamp as a date, or "—" when the
// provider reported 0 (unknown).
func epochOrDash(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02")
}

// printResourceOutputs renders the provisioned resources' connection details so
// the user can use the environment immediately.
func printResourceOutputs(stdout io.Writer, outputs []*providerv1.ResourceOutput) {
	if len(outputs) == 0 {
		return
	}
	fmt.Fprintln(stdout, "Connection outputs:")
	for _, o := range outputs {
		if pg := o.GetPostgres(); pg != nil {
			fmt.Fprintf(stdout, "  %s: postgres://%s@%s:%d/%s\n", o.GetLogicalName(), pg.GetUsername(), pg.GetHost(), pg.GetPort(), pg.GetDatabase())
			continue
		}
		if b := o.GetBucket(); b != nil {
			fmt.Fprintf(stdout, "  %s: bucket %s at %s\n", o.GetLogicalName(), b.GetBucket(), b.GetAddress())
			continue
		}
		if f := o.GetFunction(); f != nil {
			fmt.Fprintf(stdout, "  %s: %s\n", o.GetLogicalName(), f.GetUrl())
		}
	}
}
