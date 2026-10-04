package domain

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/readiness"
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
		Short: "Manage this project's production hostnames, and the domain every project's previews are served on",
		Long: "Manage this project's production hostnames, and the bootstrap-wide domain every project's previews are served on.\n\n" +
			"`add`, `rm`, `ls` and `status` are project-scoped and read domains.production, which is the declaration: " +
			"no command edits it. `use` and `release` take --preview and act on the bootstrap, where " +
			"the edge serves every project bootstrapped into the preview tier on one wildcard, at \"<project>--<preview>[--<app>].<domain>\". A project that declares its own " +
			"domains.preview keeps it and ignores this one.",
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
	return fmt.Errorf("`%s` needs --preview: a global domain is preview-only — a production hostname belongs to one project and is declared in that project's %s, so there is no global production domain to manage",
		command, project.DefaultFileName)
}

func globalPreviewBaseDomain(wildcard string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(wildcard))
	if err := project.ValidatePreviewDomain(host); err != nil {
		return "", err
	}
	return project.PreviewBaseDomain(host), nil
}

func readDomain(ctx context.Context, invocation commands.Invocation, cfg *project.Project, command string, tier environmentv1.Tier, reading progress.Title, read func(context.Context, contractv1connect.ProviderServiceClient) error) error {
	return invocation.WithProvider(ctx, cfg, command, commands.OpenOptions{Tier: tier, Require: readiness.Features}, func(ctx context.Context, p commands.ProviderRun) error {
		span := p.Check.Child(cfg.Slug, reading)
		err := p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error { return read(ctx, client) })
		span.End(err)
		return err
	})
}

func wildcardOf(base string) string {
	return "*." + base
}
