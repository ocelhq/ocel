package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/consolecredential"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
)

func (i Invocation) startWithConsoleCredential(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions) (*providerprocess.Provider, string, error) {
	launch := providerprocess.Launch{Pinning: executables.ChoosePinning(opts.DryRun)}
	provider, err := i.startProvider(ctx, check, cfg, launch)
	if err != nil || !opts.ConsoleCredential || !i.ConsoleCredential.IsRequested() || !i.ConsoleCredential.IsWantedBy(provider.Facts().GetWebIdentity()) {
		return provider, "", err
	}
	tier := readiness.TierName(opts.Tier)
	issuing := check.Child(providerID(cfg), progress.Title{Started: "Asking the console for a " + tier + " credential", Ended: "The console issued a " + tier + " credential"})
	credential, err := i.ConsoleCredential.Issue(ctx, consolecredential.Request{Tier: opts.Tier, Vendor: providerID(cfg)}, check.Warn)
	if errors.Is(err, consolecredential.ErrNoTarget) {
		issuing.End(nil)
		check.Say(fmt.Sprintf("The console binds no target to this project's %s tier, so this deploys with the credentials around it.", tier))
		return provider, "", nil
	}
	issuing.End(err)
	provider.Close()
	if err != nil {
		return nil, "", err
	}
	launch.WebIdentity = credential.WebIdentity()
	launch.Release = func() {
		if err := credential.Close(); err != nil {
			check.Warn(fmt.Sprintf("The console's credential was last renewed with an error: %v", err))
		}
	}
	provider, err = i.startProvider(ctx, check, cfg, launch)
	if err != nil {
		_ = credential.Close()
		return nil, "", err
	}
	check.Say(fmt.Sprintf("Deploying %s to account %s in %s with a credential the console issued.", tier, credential.Account(), credential.Region()))
	return provider, credential.Account(), nil
}

func (i Invocation) startProvider(ctx context.Context, check *run.Span, cfg *project.Project, launch providerprocess.Launch) (*providerprocess.Provider, error) {
	loading := check.Child(providerID(cfg), progress.Loading.Title("the provider"))
	provider, err := providerprocess.Start(ctx, cfg, check, i.Questions, launch)
	loading.End(err)
	return provider, err
}
