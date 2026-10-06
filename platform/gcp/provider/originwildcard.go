package gcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const originWildcardRecord = "origin-wildcard"

const claimAttempts = 8

type originWildcard struct {
	BaseDomain  string               `json:"baseDomain"`
	Certificate provider.Certificate `json:"certificate,omitzero"`
	Certified   bool                 `json:"certified,omitempty"`
	Address     string               `json:"address,omitempty"`
}

type originBalancer interface {
	ReconcileOriginWildcard(ctx context.Context, spec alb.OriginWildcardSpec) (string, error)
	DestroyOriginWildcard(ctx context.Context, tier environment.Tier) error
}

type originWildcards struct {
	keyValues     keyvalue.Store
	certificates  provider.Certificates
	openDNS       func() edge.DNSRecords
	balancer      originBalancer
	readWorkerCAs func(ctx context.Context, tier environment.Tier) ([]string, error)
}

func originWildcardKey(tier environment.Tier) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(cloudflare.Kind), originWildcardRecord)
}

func (w originWildcards) read(ctx context.Context, tier environment.Tier) (keyvalue.Entry, originWildcard, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, w.keyValues, originWildcardKey(tier))
	if err != nil {
		return keyvalue.Entry{}, originWildcard{}, fmt.Errorf("read which origin domain tier %s reaches its deployments under: %w", tier, err)
	}
	var recorded originWildcard
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return keyvalue.Entry{}, originWildcard{}, fmt.Errorf("decode which origin domain tier %s reaches its deployments under: %w", tier, err)
		}
	}
	return entry, recorded, nil
}

func (w originWildcards) write(ctx context.Context, tier environment.Tier, entry keyvalue.Entry, recorded originWildcard) error {
	var err error
	if entry.Value, err = json.Marshal(recorded); err != nil {
		return err
	}
	if _, err := w.keyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record which origin domain tier %s reaches its deployments under: %w", tier, err)
	}
	return nil
}

func (w originWildcards) update(ctx context.Context, tier environment.Tier, change func(*originWildcard) error) (originWildcard, error) {
	for range claimAttempts {
		entry, recorded, err := w.read(ctx, tier)
		if err != nil {
			return originWildcard{}, err
		}
		changed := recorded
		if err := change(&changed); err != nil {
			return originWildcard{}, err
		}
		if changed.BaseDomain == recorded.BaseDomain && changed.Certificate.ID == recorded.Certificate.ID &&
			changed.Certified == recorded.Certified && changed.Address == recorded.Address &&
			slices.Equal(changed.Certificate.Written, recorded.Certificate.Written) {
			return recorded, nil
		}
		err = w.write(ctx, tier, entry, changed)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return originWildcard{}, err
		}
		return changed, nil
	}
	return originWildcard{}, fmt.Errorf("record which origin domain tier %s reaches its deployments under: it changed under every one of %d attempts", tier, claimAttempts)
}

func (w originWildcards) ensure(ctx context.Context, tier environment.Tier, base string, warn func(string)) error {
	if warn == nil {
		warn = func(string) {}
	}
	dns := w.openDNS()
	hostname := "*." + base
	recorded, err := w.update(ctx, tier, func(held *originWildcard) error {
		if held.BaseDomain != "" && held.BaseDomain != base {
			return refusal.Refuse(refusal.CodeInvalid,
				"tier %s already reaches its deployments under %s, and one tier has one origin domain: set edge.cloudflare.originDomain to %s, or take this tier's Cloudflare edge bootstrap down to change it",
				tier, held.BaseDomain, held.BaseDomain)
		}
		held.BaseDomain = base
		return nil
	})
	if err != nil {
		return err
	}
	if !recorded.Certified {
		if err := w.certify(ctx, tier, hostname, recorded.Certificate, dns, warn); err != nil {
			return err
		}
		if _, recorded, err = w.read(ctx, tier); err != nil {
			return err
		}
	}
	authorities, err := w.readWorkerCAs(ctx, tier)
	if err != nil {
		return err
	}
	address, err := w.balancer.ReconcileOriginWildcard(ctx, alb.OriginWildcardSpec{
		Tier: tier, BaseDomain: base, Certificate: recorded.Certificate.ID, ClientCAs: authorities,
	})
	if err != nil {
		return err
	}
	pointer := edge.Record{Name: hostname, Type: edge.RecordTypeA, Value: address, Proxied: false}
	written, err := dns.Ensure(ctx, []edge.Record{pointer}, warn)
	if err != nil {
		return err
	}
	if !slices.Contains(written, pointer) {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s already has a DNS record ocel did not write, so the Cloudflare worker cannot reach the deployments under %s: remove that record, or name another originDomain",
			hostname, base)
	}
	_, err = w.update(ctx, tier, func(held *originWildcard) error {
		held.Address = address
		return nil
	})
	return err
}

func (w originWildcards) trustWorkerClientCertificate(ctx context.Context, tier environment.Tier, offer edge.Offer) error {
	_, recorded, err := w.read(ctx, tier)
	if err != nil || recorded.BaseDomain == "" || !recorded.Certified {
		return err
	}
	_, err = w.balancer.ReconcileOriginWildcard(ctx, alb.OriginWildcardSpec{
		Tier: tier, BaseDomain: recorded.BaseDomain, Certificate: recorded.Certificate.ID,
		ClientCAs: splitAuthorities(offer.Values[edge.OfferKeyClientCertificateAuthorities]),
	})
	return err
}

func (w originWildcards) certify(
	ctx context.Context,
	tier environment.Tier,
	hostname string,
	current provider.Certificate,
	dns edge.DNSRecords,
	warn func(string),
) error {
	record := func(cert provider.Certificate, certified bool) error {
		_, err := w.update(ctx, tier, func(held *originWildcard) error {
			held.Certificate, held.Certified = cert, certified
			return nil
		})
		return err
	}
	cert, err := w.certificates.Issue(ctx, provider.CertificateRequest{
		Kind:     cloudflare.Kind,
		Router:   router.Kind(alb.Kind),
		Hostname: hostname,
		Current:  current,
		Prove: func(ctx context.Context, cert provider.Certificate, records []edge.Record) (provider.Certificate, error) {
			written, err := dns.Ensure(ctx, records, warn)
			cert.Written = written
			return cert, err
		},
	})
	if !cert.Issued() {
		return err
	}
	if recordErr := record(cert, err == nil); recordErr != nil {
		return errors.Join(err, recordErr)
	}
	return err
}

func (w originWildcards) destroy(ctx context.Context, tier environment.Tier) error {
	_, recorded, err := w.read(ctx, tier)
	if err != nil || recorded.BaseDomain == "" {
		return err
	}
	served, err := w.servedBehindWorker(ctx, tier)
	if err != nil {
		return err
	}
	if len(served) > 0 {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s still serves the deployments of %s behind the Cloudflare worker on the %s load balancer of tier %s, and releasing its certificate would leave every one of them "+
				"unreachable: take those projects down with `ocel destroy` first",
			"*."+recorded.BaseDomain, strings.Join(served, ", "), alb.Kind, tier)
	}
	if err := w.balancer.DestroyOriginWildcard(ctx, tier); err != nil {
		return err
	}
	owned := slices.Clone(recorded.Certificate.Written)
	if recorded.Address != "" {
		owned = append(owned, edge.Record{Name: "*." + recorded.BaseDomain, Type: edge.RecordTypeA, Value: recorded.Address})
	}
	if len(owned) > 0 {
		if err := w.openDNS().Delete(ctx, owned); err != nil {
			return err
		}
	}
	if err := w.certificates.Discard(ctx, recorded.Certificate, progress.Discard()); err != nil {
		return err
	}
	return keyvalue.Forget(ctx, w.keyValues, originWildcardKey(tier))
}

func (w originWildcards) servedBehindWorker(ctx context.Context, tier environment.Tier) ([]string, error) {
	entries, err := w.keyValues.List(ctx, stackrecords.EdgeStacksPartition(tier))
	if err != nil {
		return nil, fmt.Errorf("read which projects the %s edge serves on tier %s: %w", cloudflare.Kind, tier, err)
	}
	var served []string
	for _, entry := range entries {
		if len(entry.Key.Path) != 1 {
			continue
		}
		var state stackrecords.EdgeState
		if err := json.Unmarshal(entry.Value, &state); err != nil {
			return nil, fmt.Errorf("decode what the %s edge serves for %s: %w", cloudflare.Kind, entry.Key.Path[0], err)
		}
		if state.Kind == cloudflare.Kind {
			served = append(served, entry.Key.Path[0])
		}
	}
	slices.Sort(served)
	return served, nil
}

func splitAuthorities(bundle string) []string {
	var authorities []string
	rest := []byte(bundle)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return authorities
		}
		authorities = append(authorities, string(pem.EncodeToMemory(block)))
	}
}

func (p *Provider) readWorkerCAs(ctx context.Context, tier environment.Tier) ([]string, error) {
	c, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	adopted, _, err := readAdoptedEdge(ctx, c, p.KeyValues(), tier, cloudflare.Kind)
	if err != nil {
		return nil, err
	}
	authorities := splitAuthorities(adopted.ClientCertificate.Authorities)
	if len(authorities) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge's bootstrap adopted no client certificate authority, and the load balancer trusts the worker only through it: run `%s` again",
			cloudflare.Kind, provider.BootstrapCommand(tier))
	}
	return authorities, nil
}
