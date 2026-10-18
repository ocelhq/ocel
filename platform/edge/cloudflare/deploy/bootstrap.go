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
	state, err := p.readState(ctx, accountID, tier)
	if err != nil {
		return edge.BootstrapOutput{}, err
	}
	out := edge.BootstrapOutput{Trust: edge.TrustExternal}
	for _, worker := range state.workers {
		offer, err := p.ensureWorker(ctx, accountID, worker)
		if err != nil {
			return out, fmt.Errorf("bootstrap %s: %w", worker.what, err)
		}
		out.Offers = append(out.Offers, offer)
	}
	if p.workerClientCertificate {
		offer, err := p.workerClientCertificates().ensure(ctx, accountID, state.certificates)
		if err != nil {
			return out, fmt.Errorf("bootstrap the worker client certificate: %w", err)
		}
		out.Offers = append(out.Offers, offer)
		if p.trustClientCertificate != nil {
			if err := p.trustClientCertificate(ctx, tier, offer); err != nil {
				return out, fmt.Errorf("make the origin trust the worker client certificate's CA before the entry worker presents it: %w", err)
			}
		}
	}
	return out, nil
}

func (p *cloudflare) Teardown(ctx context.Context, tier environment.Tier) error {
	accountID, err := requireAccountID("tear the Cloudflare edge down")
	if err != nil {
		return err
	}
	storeScript, err := storeScriptNameFor(p.namespace, tier)
	if err != nil {
		return err
	}
	var errs []error
	if err := p.deleteScript(ctx, accountID, storeScript); err != nil {
		errs = append(errs, fmt.Errorf("delete worker %q: %w", storeScript, err))
	}
	if p.workerClientCertificate {
		if err := p.workerClientCertificates().teardown(ctx, accountID, tier); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type bootstrapWorker struct {
	scriptName string
	worker     workerScript
	do         durableObjectWorker
	what       string
	offer      edge.OfferKind
	keys       offerKeys
}

type offerKeys struct {
	endpoint   string
	scriptName string
	cred       string
}

func bootstrapWorkers(namespace string, tier environment.Tier) ([]bootstrapWorker, error) {
	storeScript, err := storeScriptNameFor(namespace, tier)
	if err != nil {
		return nil, err
	}
	storeWorker := workerScript{Main: mainModule(releasesStoreBundle)}

	return []bootstrapWorker{
		{
			scriptName: storeScript,
			worker:     storeWorker,
			do:         releasesStoreWorker,
			what:       "releases-store worker",
			offer:      edge.OfferReleasesStore,
			keys: offerKeys{
				endpoint:   edge.OfferKeyStoreEndpoint,
				scriptName: edge.OfferKeyStoreScriptName,
				cred:       edge.OfferKeyStoreBootstrapCredential,
			},
		},
	}, nil
}

func (p *cloudflare) ensureWorker(ctx context.Context, accountID string, state workerState) (edge.Offer, error) {
	b := state.bootstrapWorker
	up := upload{accountID: accountID, scriptName: b.scriptName, worker: b.worker}
	var cred string
	if !state.secretPresent {
		minted, err := mintSecret()
		if err != nil {
			return edge.Offer{}, fmt.Errorf("mint bootstrap credential: %w", err)
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
			return edge.Offer{}, fmt.Errorf("put %s: %w", b.what, err)
		}
	case cred != "":
		if err := p.putBootstrapSecret(ctx, accountID, b.scriptName, cred); err != nil {
			return edge.Offer{}, fmt.Errorf("set the bootstrap credential of %s: %w", b.what, err)
		}
	}

	endpoint, err := p.ensureSubdomain(ctx, up, state.subdomainOn, b.what)
	if err != nil {
		return edge.Offer{}, err
	}

	values := map[string]string{
		b.keys.endpoint:   endpoint,
		b.keys.scriptName: b.scriptName,
	}
	if cred != "" {
		values[b.keys.cred] = cred
	}
	return edge.Offer{Kind: b.offer, Values: values}, nil
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
