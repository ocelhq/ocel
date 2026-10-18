package cloudflareorigin

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
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const originWildcardRecord = "origin-wildcard"

const recordAttempts = 8

type Wildcard struct {
	BaseDomain  string               `json:"baseDomain"`
	Certificate provider.Certificate `json:"certificate,omitzero"`
	Certified   bool                 `json:"certified,omitempty"`
	Address     string               `json:"address,omitempty"`
}

type originBalancer interface {
	ReconcileOriginWildcard(ctx context.Context, spec alb.OriginWildcardSpec) (string, error)
	DestroyOriginWildcard(ctx context.Context, tier environment.Tier) error
}

type Wildcards struct {
	KeyValues     records.Store
	Certificates  provider.Certificates
	OpenDNS       func() edge.DNSRecords
	Balancer      originBalancer
	ReadWorkerCAs func(ctx context.Context, tier environment.Tier) ([]string, error)
}

func WildcardKey(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(cloudflare.Kind), originWildcardRecord)
}

func (w Wildcards) Read(ctx context.Context, tier environment.Tier) (records.Entry, Wildcard, error) {
	entry, err := records.ReadOrEmpty(ctx, w.KeyValues, WildcardKey(tier))
	if err != nil {
		return records.Entry{}, Wildcard{}, fmt.Errorf("read which origin domain tier %s reaches its deployments under: %w", tier, err)
	}
	var recorded Wildcard
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return records.Entry{}, Wildcard{}, fmt.Errorf("decode which origin domain tier %s reaches its deployments under: %w", tier, err)
		}
	}
	return entry, recorded, nil
}

func (w Wildcards) write(ctx context.Context, tier environment.Tier, entry records.Entry, recorded Wildcard) error {
	var err error
	if entry.Value, err = json.Marshal(recorded); err != nil {
		return err
	}
	if _, err := w.KeyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record which origin domain tier %s reaches its deployments under: %w", tier, err)
	}
	return nil
}

func (w Wildcards) update(ctx context.Context, tier environment.Tier, change func(*Wildcard) error) (Wildcard, error) {
	for range recordAttempts {
		entry, recorded, err := w.Read(ctx, tier)
		if err != nil {
			return Wildcard{}, err
		}
		changed := recorded
		if err := change(&changed); err != nil {
			return Wildcard{}, err
		}
		if changed.BaseDomain == recorded.BaseDomain && changed.Certificate.ID == recorded.Certificate.ID &&
			changed.Certified == recorded.Certified && changed.Address == recorded.Address &&
			slices.Equal(changed.Certificate.Written, recorded.Certificate.Written) {
			return recorded, nil
		}
		err = w.write(ctx, tier, entry, changed)
		if errors.Is(err, records.ErrStale) {
			continue
		}
		if err != nil {
			return Wildcard{}, err
		}
		return changed, nil
	}
	return Wildcard{}, fmt.Errorf("record which origin domain tier %s reaches its deployments under: it changed under every one of %d attempts", tier, recordAttempts)
}

func (w Wildcards) Ensure(ctx context.Context, tier environment.Tier, base string, warn func(string)) error {
	if warn == nil {
		warn = func(string) {}
	}
	dns := w.OpenDNS()
	hostname := "*." + base
	recorded, err := w.update(ctx, tier, func(held *Wildcard) error {
		if held.BaseDomain != "" && held.BaseDomain != base {
			return refusal.Refuse(refusal.CodeInvalid,
				"tier %s already reaches its deployments under %s, and one tier has one origin domain: set provider.gcp.edge.cloudflare.originDomain to %s, or take this tier's Cloudflare edge bootstrap down to change it",
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
		if _, recorded, err = w.Read(ctx, tier); err != nil {
			return err
		}
	}
	authorities, err := w.ReadWorkerCAs(ctx, tier)
	if err != nil {
		return err
	}
	address, err := w.Balancer.ReconcileOriginWildcard(ctx, alb.OriginWildcardSpec{
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
	_, err = w.update(ctx, tier, func(held *Wildcard) error {
		held.Address = address
		return nil
	})
	return err
}

func (w Wildcards) TrustWorkerClientCertificate(ctx context.Context, tier environment.Tier, offer edge.Offer) error {
	_, recorded, err := w.Read(ctx, tier)
	if err != nil || recorded.BaseDomain == "" || !recorded.Certified {
		return err
	}
	_, err = w.Balancer.ReconcileOriginWildcard(ctx, alb.OriginWildcardSpec{
		Tier: tier, BaseDomain: recorded.BaseDomain, Certificate: recorded.Certificate.ID,
		ClientCAs: splitAuthorities(offer.Values[edge.OfferKeyClientCertificateAuthorities]),
	})
	return err
}

func (w Wildcards) certify(
	ctx context.Context,
	tier environment.Tier,
	hostname string,
	current provider.Certificate,
	dns edge.DNSRecords,
	warn func(string),
) error {
	record := func(cert provider.Certificate, certified bool) error {
		_, err := w.update(ctx, tier, func(held *Wildcard) error {
			held.Certificate, held.Certified = cert, certified
			return nil
		})
		return err
	}
	cert, err := w.Certificates.Issue(ctx, provider.CertificateRequest{
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

func (w Wildcards) Destroy(ctx context.Context, tier environment.Tier) error {
	_, recorded, err := w.Read(ctx, tier)
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
	if err := w.Balancer.DestroyOriginWildcard(ctx, tier); err != nil {
		return err
	}
	owned := slices.Clone(recorded.Certificate.Written)
	if recorded.Address != "" {
		owned = append(owned, edge.Record{Name: "*." + recorded.BaseDomain, Type: edge.RecordTypeA, Value: recorded.Address})
	}
	if len(owned) > 0 {
		if err := w.OpenDNS().Delete(ctx, owned); err != nil {
			return err
		}
	}
	if err := w.Certificates.Discard(ctx, recorded.Certificate, progress.Discard()); err != nil {
		return err
	}
	return records.Forget(ctx, w.KeyValues, WildcardKey(tier))
}

func (w Wildcards) servedBehindWorker(ctx context.Context, tier environment.Tier) ([]string, error) {
	entries, err := w.KeyValues.List(ctx, stackrecords.EdgeStacksPartition(tier))
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

func (p Offers) ReadWorkerCAs(ctx context.Context, tier environment.Tier) ([]string, error) {
	c, err := p.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	adopted, _, err := RequireAdoptedEdge(ctx, c, p.KeyValues, tier, cloudflare.Kind)
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
