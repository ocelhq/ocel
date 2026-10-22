package cloudflare

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

func (p *cloudflare) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	names, err := p.names(ctx)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	if err := p.recordBootstrapAccount(ctx, tier, accountID); err != nil {
		return edge.BootstrapOutput{}, err
	}
	state, err := p.readState(ctx, accountID, names, tier)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	out := edge.BootstrapOutput{Trust: edge.TrustExternal}
	for _, worker := range state.workers {
		store, err := p.ensureWorker(ctx, accountID, worker)
		if err != nil {
			return out, fmt.Errorf("bootstrap %s: %w", worker.what, err)
		}
		out.ReleasesStore = &store
	}
	return out, nil
}

func (p *cloudflare) Teardown(ctx context.Context, tier environment.Tier) error {
	accountID, err := p.tierAccount(ctx, tier, "tear the Cloudflare edge down")
	if err != nil {
		return err
	}
	names, err := p.names(ctx)
	if err != nil {
		return err
	}
	storeScript, err := names.storeScript(tier)
	if err != nil {
		return err
	}
	var errs []error
	if err := p.deleteScript(ctx, accountID, storeScript); err != nil {
		errs = append(errs, fmt.Errorf("delete worker %q: %w", storeScript, err))
	}
	if err := p.deleteClientCertificates(ctx, accountID, certificateOwner{origin: names.origin, tier: tier}); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return p.forgetTierRecord(ctx, tier)
}

type bootstrapWorker struct {
	scriptName string
	worker     workerScript
	do         durableObjectWorker
	what       string
}

func (n originNames) bootstrapWorkers(tier environment.Tier) ([]bootstrapWorker, error) {
	storeScript, err := n.storeScript(tier)
	if err != nil {
		return nil, err
	}
	storeWorker := workerScript{Main: mainModule(releasesStoreBundle), Tags: []string{n.origin.scriptTag()}}

	return []bootstrapWorker{
		{
			scriptName: storeScript,
			worker:     storeWorker,
			do:         releasesStoreWorker,
			what:       "releases-store worker",
		},
	}, nil
}

func (p *cloudflare) ensureWorker(ctx context.Context, accountID string, state workerState) (edge.ReleasesStore, error) {
	b := state.bootstrapWorker
	up := upload{accountID: accountID, scriptName: b.scriptName, worker: b.worker}
	var cred string
	if !state.secretPresent {
		minted, err := mintSecret()
		if err != nil {
			return edge.ReleasesStore{}, fmt.Errorf("mint bootstrap credential: %w", err)
		}
		cred = minted
	}

	switch {
	case !state.upToDate():
		var inherited []string
		if cred == "" {
			inherited = []string{bootstrapSecretBinding}
		} else {
			up.worker = withSecret(b.worker, bootstrapSecretBinding, cred)
		}
		if err := p.putDurableObjectScript(ctx, up, b.do, state.classes, inherited); err != nil {
			return edge.ReleasesStore{}, fmt.Errorf("put %s: %w", b.what, err)
		}
	case cred != "":
		if err := p.putBootstrapSecret(ctx, accountID, b.scriptName, cred); err != nil {
			return edge.ReleasesStore{}, fmt.Errorf("set the bootstrap credential of %s: %w", b.what, err)
		}
	}

	endpoint, err := p.ensureSubdomain(ctx, up, state.subdomainOn, b.what)
	if err != nil {
		return edge.ReleasesStore{}, err
	}

	return edge.ReleasesStore{Name: b.scriptName, Endpoint: endpoint, BootstrapCredential: cred}, nil
}

func (p *cloudflare) ensureSubdomain(ctx context.Context, up upload, on bool, what string) (string, error) {
	if !on {
		endpoint, err := p.setSubdomain(ctx, up, true)
		if err != nil {
			return "", fmt.Errorf("set %s subdomain: %w", what, err)
		}
		return endpoint, nil
	}
	endpoint, err := p.subdomainURL(ctx, up.accountID, up.scriptName)
	if err != nil {
		return "", fmt.Errorf("read %s subdomain: %w", what, err)
	}
	return endpoint, nil
}
