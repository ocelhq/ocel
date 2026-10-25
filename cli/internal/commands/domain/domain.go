package domain

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type domainOptions struct {
	preview bool
	yes     bool
	wait    bool
}

func NewCommand(invocation commands.Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage production hostnames and the global preview domain",
		Long: "Manage this project's production hostnames, and the bootstrap-wide domain every project's previews are served on.\n\n" +
			"`add`, `rm`, `ls` and `status` read this project's domains.production; no command edits it. " +
			"`use` and `release` take --preview and set the wildcard every previewed project is served on, " +
			"at \"<project>--<preview>[--<app>].<domain>\". A project that declares its own " +
			"domains.preview keeps it.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		commands.ReserveStdout(newStatusCommand(invocation)),
		newAddCommand(invocation),
		newRemoveCommand(invocation),
		newUseCommand(invocation),
		commands.ReserveStdout(newListCommand(invocation)),
		newReleaseCommand(invocation),
	)
	return commands.DeclareReadOnly(cmd)
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func requirePreviewTier(command string, preview bool) error {
	if preview {
		return nil
	}
	return fmt.Errorf("`%s` needs --preview: the global domain is preview-only",
		command)
}

func globalPreviewBaseDomain(wildcard string) (string, error) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(wildcard)), ".")
	if err := project.ValidatePreviewDomain(host); err != nil {
		return "", err
	}
	return project.PreviewBaseDomain(host), nil
}

func readDomain(ctx context.Context, invocation commands.Invocation, target commands.Target, command string, tier environmentv1.Tier, reading progress.Title, read func(context.Context, contractv1connect.ProviderServiceClient) error) error {
	return invocation.WithProvider(ctx, target.Project, command, commands.OpenOptions{Intent: commands.UseParts, Tier: tier, Cloud: target.Cloud}, func(ctx context.Context, p commands.ProviderRun) error {
		span := p.Check.Child(target.Project.Slug, reading)
		err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error { return read(ctx, client) })
		span.End(err)
		return err
	})
}

func wildcardOf(base string) string {
	return "*." + base
}
