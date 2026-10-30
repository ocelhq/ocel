package protection

import (
	"context"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

const (
	minPasswordChars = 12
	maxPasswordBytes = 256
)

func newPasswordCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Show the password that protects production, or the previews with --preview",
		Long: "Show the password that protects this project's production, or its previews with `--preview`.\n\n" +
			"The password is created by the first deploy of a tier whose config turns protection on, and it is " +
			"kept so it can be shown again. Production and previews have a password each. Only the password is " +
			"printed on stdout, so it can be piped. `ocel protection password rotate` replaces it with a generated " +
			"one, and `ocel protection password set` with one you choose.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReveal(cmd.Context(), invocation, t, cmd.OutOrStdout())
		},
	}
	t.bind(cmd)
	cmd.AddCommand(newRotateCommand(invocation), newSetCommand(invocation))
	return commands.DeclareResult(commands.DeclareReadOnly(commands.ReserveStdout(cmd)), &resultv1.PasswordResult{})
}

func runReveal(ctx context.Context, invocation commands.Invocation, t target, stdout io.Writer) error {
	cfg, err := loadProject(ctx, invocation)
	if err != nil {
		return err
	}
	if err := requireProtection(cfg, t); err != nil {
		return err
	}
	var status *contractv1.GetProtectionResponse
	var revealed *contractv1.RevealProtectionPasswordResponse
	err = invocation.WithProvider(ctx, cfg, "ocel protection password", openOptions(t.tier()), func(ctx context.Context, p commands.ProviderRun) (err error) {
		spanCtx, span := p.Check.Child(ctx, cfg.Slug, progress.Reading.Title("the "+t.name()+" protection"))
		defer func() { span.End(err) }()
		if status, err = getProtection(spanCtx, p, cfg, t.tier()); err != nil || !status.GetRecorded() || !status.GetEnabled() {
			return err
		}
		return p.Provider.Call(spanCtx, func(client contractv1connect.ProviderServiceClient) (err error) {
			revealed, err = client.RevealProtectionPassword(spanCtx, &contractv1.RevealProtectionPasswordRequest{Tier: t.tier(), Slug: cfg.Slug})
			return err
		})
	})
	if err != nil {
		return err
	}
	if err := refuseOpened(t, cfg.Slug, status); err != nil {
		return err
	}
	if !revealed.GetRecorded() {
		return fmt.Errorf("%s has no %s password yet: the first %s deploy creates it", cfg.Slug, t.name(), t.name())
	}
	result := &resultv1.PasswordResult{Slug: cfg.Slug, Tier: t.tier(), Password: revealed.GetPassword(), Edges: edgeResults(status.GetEdges())}
	return printSecret(invocation, stdout, revealed.GetPassword(), result)
}

func newRotateCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "rotate",
		Short: "Replace the password with a generated one, and print it",
		Long: "Replace this project's production password, or its preview password with `--preview`, with a generated one, and print it.\n\n" +
			"The old password stops working, and so does every login session it opened: a visitor enters " +
			"the new password. Bypass secrets keep working, and every login session ends with the old password, " +
			"including those a bypass secret opened. The other tier's password is untouched. The change is " +
			"published to the edge that serves the tier without a deploy. Only the password is printed on stdout.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcome, err := applyChange(cmd.Context(), invocation, t, "ocel protection password rotate",
				&contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_RotatePassword{RotatePassword: &contractv1.RotatePassword{}}},
				func(outcome changeOutcome) string {
					return fmt.Sprintf("Rotated the %s password of %s and ended its login sessions. Run `ocel protection password%s` to see it again", t.name(), outcome.slug, t.flag())
				})
			password := outcome.changed.GetPassword()
			if password == "" {
				return err
			}
			result := &resultv1.PasswordResult{Slug: outcome.slug, Tier: outcome.tier, Password: password, Edges: outcome.edges()}
			return failAfterResult(invocation, cmd.OutOrStdout(), err, printSecret(invocation, cmd.OutOrStdout(), password, result))
		},
	}
	t.bind(cmd)
	return commands.DeclareResult(commands.DeclareMutating(commands.ReserveStdout(cmd)), &resultv1.PasswordResult{})
}

func newSetCommand(invocation commands.Invocation) *cobra.Command {
	var t target
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Replace the password with one read from stdin",
		Long: "Replace this project's production password, or its preview password with `--preview`, with one read from stdin.\n\n" +
			"The password is read from stdin, never from an argument, so it stays out of shell history and " +
			"process listings. It needs at least 12 characters and at most 256 bytes. The old password and " +
			"every login session end, as with `rotate`:\n\n" +
			"  printf '%s' \"$PASSWORD\" | ocel protection password set",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			password, err := readPasswordFrom(invocation, t, cmd.InOrStdin())
			if err != nil {
				return err
			}
			outcome, err := applyChange(cmd.Context(), invocation, t, "ocel protection password set",
				&contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_SetPassword{SetPassword: &contractv1.SetPassword{Password: password}}},
				func(outcome changeOutcome) string {
					return fmt.Sprintf("Set the %s password of %s and ended its login sessions", t.name(), outcome.slug)
				})
			return printPublication(invocation, cmd.OutOrStdout(), outcome, err)
		},
	}
	t.bind(cmd)
	return commands.DeclareResult(commands.DeclareMutating(commands.PrintOnlyUnderJSON(cmd)), &resultv1.PublicationResult{})
}

func readPasswordFrom(invocation commands.Invocation, t target, stdin io.Reader) (string, error) {
	if invocation.StdinIsTerminal(stdin) {
		return "", fmt.Errorf("`ocel protection password set%[1]s` reads the password from standard input, and standard input is a terminal: pipe it in, as in `printf '%%s' \"$PASSWORD\" | ocel protection password set%[1]s`, or have one generated with `ocel protection password rotate%[1]s`", t.flag())
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, int64(maxPasswordBytes+len("\r\n")+1)))
	if err != nil {
		return "", fmt.Errorf("read the password from standard input: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	switch {
	case password == "":
		return "", fmt.Errorf("standard input held no password: pipe the password in, or have one generated with `ocel protection password rotate%s`", t.flag())
	case len(password) > maxPasswordBytes:
		return "", fmt.Errorf("a %s password is at most %d bytes", t.name(), maxPasswordBytes)
	case utf8.RuneCountInString(password) < minPasswordChars:
		return "", fmt.Errorf("a %s password needs at least %d characters, and this one has %d", t.name(), minPasswordChars, utf8.RuneCountInString(password))
	}
	return password, nil
}
