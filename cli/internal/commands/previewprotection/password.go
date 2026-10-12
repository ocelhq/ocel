package previewprotection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	minPasswordChars = 12
	maxPasswordBytes = 256
)

func newPasswordCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Show the password that protects this project's previews",
		Long: "Show the password that protects this project's previews.\n\n" +
			"The password is created by the first preview deploy of a project whose config turns " +
			"previews.protection on, and it is kept so it can be shown again. Only the password is printed on " +
			"stdout, so it can be piped. `ocel preview protection password rotate` replaces it with a generated " +
			"one, and `ocel preview protection password set` with one you choose.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReveal(cmd.Context(), invocation, cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(newRotateCommand(invocation), newSetCommand(invocation))
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.PreviewPasswordResult{})
}

func runReveal(ctx context.Context, invocation commands.Invocation, stdout io.Writer) error {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	if err := requireProtection(cfg); err != nil {
		return err
	}
	resp, err := readProtection(ctx, invocation, cfg, "ocel preview protection password", true)
	if err != nil {
		return err
	}
	if err := refuseOpenedPreviews(cfg.Slug, resp); err != nil {
		return err
	}
	if !resp.GetRecorded() {
		return fmt.Errorf("%s has no preview password yet: the first preview deploy creates it", cfg.Slug)
	}
	result := &resultv1.PreviewPasswordResult{Slug: cfg.Slug, Password: resp.GetPassword(), Edges: edgeResults(resp.GetEdges())}
	return printSecret(invocation, stdout, resp.GetPassword(), result)
}

func newRotateCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Replace this project's preview password with a generated one, and print it",
		Long: "Replace this project's preview password with a generated one, and print it.\n\n" +
			"The old password stops working, and so does every login session it opened: a visitor enters " +
			"the new password. Bypass secrets keep working, and every login session ends with the old password, " +
			"including those a bypass secret opened. The change is published to the edge that serves the " +
			"project's previews without a deploy. Only the password is printed on stdout.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcome, err := applyChange(cmd.Context(), invocation, "ocel preview protection password rotate",
				&contractv1.ChangePreviewProtectionRequest{Change: &contractv1.ChangePreviewProtectionRequest_RotatePassword{RotatePassword: &contractv1.RotatePreviewPassword{}}},
				func(outcome changeOutcome) string {
					return fmt.Sprintf("Rotated the preview password of %s and ended its login sessions. Run `ocel preview protection password` to see it again", outcome.slug)
				})
			password := outcome.changed.GetPassword()
			if password == "" {
				return err
			}
			result := &resultv1.PreviewPasswordResult{Slug: outcome.slug, Password: password, Edges: outcome.edges()}
			return failAfterResult(invocation, cmd.OutOrStdout(), err, printSecret(invocation, cmd.OutOrStdout(), password, result))
		},
	}
	return commands.DeclareResult(commands.DeclareMutating(commands.ReserveStdout(cmd)), &resultv1.PreviewPasswordResult{})
}

func newSetCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Replace this project's preview password with one read from stdin",
		Long: "Replace this project's preview password with one read from stdin.\n\n" +
			"The password is read from stdin, never from an argument, so it stays out of shell history and " +
			"process listings. It needs at least 12 characters and at most 256 bytes. The old password and " +
			"every login session end, as with `rotate`:\n\n" +
			"  printf '%s' \"$PREVIEW_PASSWORD\" | ocel preview protection password set",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			password, err := readPasswordFrom(invocation, cmd.InOrStdin())
			if err != nil {
				return err
			}
			outcome, err := applyChange(cmd.Context(), invocation, "ocel preview protection password set",
				&contractv1.ChangePreviewProtectionRequest{Change: &contractv1.ChangePreviewProtectionRequest_SetPassword{SetPassword: &contractv1.SetPreviewPassword{Password: password}}},
				func(outcome changeOutcome) string {
					return fmt.Sprintf("Set the preview password of %s and ended its login sessions", outcome.slug)
				})
			return printPublication(invocation, cmd.OutOrStdout(), outcome, err)
		},
	}
	return commands.DeclareResult(commands.DeclareMutating(commands.PrintOnlyUnderJSON(cmd)), &resultv1.PreviewPublicationResult{})
}

func readPasswordFrom(invocation commands.Invocation, stdin io.Reader) (string, error) {
	if invocation.StdinIsTerminal(stdin) {
		return "", errors.New("`ocel preview protection password set` reads the password from standard input, and standard input is a terminal: pipe it in, as in `printf '%s' \"$PREVIEW_PASSWORD\" | ocel preview protection password set`, or have one generated with `ocel preview protection password rotate`")
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, int64(maxPasswordBytes+len("\r\n")+1)))
	if err != nil {
		return "", fmt.Errorf("read the password from standard input: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	switch {
	case password == "":
		return "", errors.New("standard input held no password: pipe the password in, or have one generated with `ocel preview protection password rotate`")
	case len(password) > maxPasswordBytes:
		return "", fmt.Errorf("a preview password is at most %d bytes", maxPasswordBytes)
	case utf8.RuneCountInString(password) < minPasswordChars:
		return "", fmt.Errorf("a preview password needs at least %d characters, and this one has %d", minPasswordChars, utf8.RuneCountInString(password))
	}
	return password, nil
}
