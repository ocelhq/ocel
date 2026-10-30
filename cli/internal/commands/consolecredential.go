package commands

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/cli/internal/consolecredential"
	"github.com/ocelhq/ocel/cli/internal/executables"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (i Invocation) startWithConsoleCredential(ctx context.Context, check *run.Span, cfg *project.Project, opts OpenOptions) (*providerprocess.Provider, string, error) {
	launch := providerprocess.Launch{Pinning: executables.ChoosePinning(opts.DryRun || i.Job() != nil)}
	if i.isJobOnCloud(cfg) {
		identity, err := i.readCloudWebIdentity(ctx)
		if err != nil {
			return nil, "", err
		}
		launch.WebIdentity = identity
		provider, err := i.startProvider(ctx, check, cfg, launch)
		return provider, "", err
	}
	if !opts.ConsoleCredential || !i.ConsoleCredential.IsRequested() {
		provider, err := i.startProvider(ctx, check, cfg, launch)
		return provider, "", err
	}
	tier := readiness.TierName(opts.Tier)
	issuingCtx, issuing := check.Child(ctx, providerID(cfg), progress.Title{Started: "Asking the console for a " + tier + " credential", Ended: "The console issued a " + tier + " credential"})
	credential, err := i.ConsoleCredential.Issue(issuingCtx, consolecredential.Request{Tier: opts.Tier, Vendor: providerID(cfg)}, check.Warn)
	if errors.Is(err, consolecredential.ErrNoTarget) {
		issuing.End(nil)
		check.Say(fmt.Sprintf("The console binds no target to this project's %s tier, so this deploys with the credentials around it.", tier))
		provider, err := i.startProvider(ctx, check, cfg, launch)
		return provider, "", err
	}
	issuing.End(err)
	if err != nil {
		return nil, "", err
	}
	if launch.Namespace, err = targetNamespace(credential.Namespace(), tier); err != nil {
		_ = credential.Close()
		return nil, "", err
	}
	launch.WebIdentity = credential.WebIdentity()
	launch.Release = func() {
		if err := credential.Close(); err != nil {
			check.Warn(fmt.Sprintf("The console's credential was last renewed with an error: %v", err))
		}
	}
	provider, err := i.startProvider(ctx, check, cfg, launch)
	if err != nil {
		_ = credential.Close()
		return nil, "", err
	}
	check.Say(issuedCredentialLine(tier, credential.Account(), credential.Region()))
	return provider, credential.Account(), nil
}

func (i Invocation) startProvider(ctx context.Context, check *run.Span, cfg *project.Project, launch providerprocess.Launch) (*providerprocess.Provider, error) {
	loadingCtx, loading := check.Child(ctx, providerID(cfg), progress.Loading.Title("the provider"))
	provider, err := providerprocess.Start(loadingCtx, cfg, check, i.Questions, launch)
	loading.End(err)
	return provider, err
}

func targetNamespace(named, tier string) (string, error) {
	if named == "" {
		return "", nil
	}
	if _, err := provider.ParseNamespace(named); err != nil {
		return "", fmt.Errorf("the console's %s target names an unusable namespace: %w", tier, err)
	}
	if set := os.Getenv(provider.NamespaceEnvVar); set != "" && set != named {
		return "", fmt.Errorf("this job runs under namespace %s (%s) and the console's %s target was bootstrapped under %s: unset %s, or bootstrap the target under %s",
			set, provider.NamespaceEnvVar, tier, named, provider.NamespaceEnvVar, set)
	}
	return named, nil
}

func issuedCredentialLine(tier, account, region string) string {
	if region == "" {
		return fmt.Sprintf("Deploying %s to account %s with a credential the console issued.", tier, account)
	}
	return fmt.Sprintf("Deploying %s to account %s in %s with a credential the console issued.", tier, account, region)
}
