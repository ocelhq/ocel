package alb

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const shieldedNameSegment = "shielded"

const (
	trustAttempts  = 8
	maxAllowlisted = 500
)

func (e *Edge) Shielded() *Edge {
	deps := e.deps
	deps.Shielded = true
	return New(deps)
}

type trustRecord struct {
	Hostnames   map[string][]string `json:"hostnames,omitempty"`
	Placeholder string              `json:"placeholder,omitempty"`
	Provisioned []string            `json:"provisioned,omitempty"`
}

func (r trustRecord) allowlist() []string {
	var trusted []string
	for _, certificates := range r.Hostnames {
		trusted = append(trusted, certificates...)
	}
	if len(trusted) == 0 && r.Placeholder != "" {
		return []string{r.Placeholder}
	}
	slices.Sort(trusted)
	return slices.Compact(trusted)
}

func (r trustRecord) withClaim(hostname string, certificates []string) trustRecord {
	r = r.withCarried(certificates)
	r.Hostnames[hostname] = sortedCertificates(certificates)
	return r
}

func (r trustRecord) withCarried(certificates []string) trustRecord {
	carried := sortedCertificates(certificates)
	hostnames := make(map[string][]string, len(r.Hostnames)+1)
	for hostname, held := range r.Hostnames {
		if slices.ContainsFunc(held, func(certificate string) bool { return slices.Contains(carried, certificate) }) {
			held = carried
		}
		hostnames[hostname] = held
	}
	r.Hostnames = hostnames
	return r
}

func (r trustRecord) withoutClaims(hostnames []string) trustRecord {
	r.Hostnames = maps.Clone(r.Hostnames)
	for _, hostname := range hostnames {
		delete(r.Hostnames, hostname)
	}
	return r
}

func sortedCertificates(certificates []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(certificates)))
}

func (e *Edge) trustKey(tier environment.Tier) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), "client-certificates")
}

func (e *Edge) readTrust(ctx context.Context, tier environment.Tier) (keyvalue.Entry, trustRecord, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.trustKey(tier))
	if err != nil {
		return keyvalue.Entry{}, trustRecord{}, fmt.Errorf("read which client certificates the %s front of tier %s trusts: %w", Kind, tier, err)
	}
	var read trustRecord
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &read); err != nil {
			return keyvalue.Entry{}, trustRecord{}, fmt.Errorf("decode which client certificates the %s front of tier %s trusts: %w", Kind, tier, err)
		}
	}
	return entry, read, nil
}

func (e *Edge) changeTrust(ctx context.Context, tier environment.Tier, change func(trustRecord) trustRecord) (trustRecord, error) {
	for range trustAttempts {
		entry, read, err := e.readTrust(ctx, tier)
		if err != nil {
			return trustRecord{}, err
		}
		changed := change(read)
		if reflect.DeepEqual(changed, read) {
			return read, nil
		}
		if entry.Value, err = json.Marshal(changed); err != nil {
			return trustRecord{}, err
		}
		_, err = e.deps.KeyValues.Write(ctx, entry)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return trustRecord{}, fmt.Errorf("record which client certificates the %s front of tier %s trusts: %w", Kind, tier, err)
		}
		return changed, nil
	}
	return trustRecord{}, fmt.Errorf("record which client certificates the %s front of tier %s trusts: it changed under every one of %d attempts", Kind, tier, trustAttempts)
}

func (e *Edge) ensureAllowlist(ctx context.Context, tier environment.Tier) ([]string, error) {
	_, record, err := e.readTrust(ctx, tier)
	if err != nil {
		return nil, err
	}
	if trusted := record.allowlist(); len(trusted) > 0 {
		return trusted, nil
	}
	placeholder, err := mintUnpresentableCertificate(time.Now())
	if err != nil {
		return nil, err
	}
	record, err = e.changeTrust(ctx, tier, func(read trustRecord) trustRecord {
		if read.Placeholder == "" {
			read.Placeholder = placeholder
		}
		return read
	})
	return record.allowlist(), err
}

func (e *Edge) trustClaim(ctx context.Context, tier environment.Tier, hostname string, certificates []string) (Front, error) {
	front, record, err := e.applyTrust(ctx, tier, func(read trustRecord) trustRecord { return read.withClaim(hostname, certificates) })
	if err != nil {
		return Front{}, err
	}
	if !front.provisioned() {
		if front, err = e.raiseTrusting(ctx, tier, record.allowlist()); err != nil {
			return Front{}, err
		}
	}
	if _, _, err := e.applyTrust(ctx, tier.Sibling(), func(read trustRecord) trustRecord { return read.withCarried(certificates) }); err != nil {
		return Front{}, err
	}
	return front, nil
}

func (e *Edge) withdrawClaims(ctx context.Context, tier environment.Tier, hostnames ...string) error {
	_, _, err := e.applyTrust(ctx, tier, func(read trustRecord) trustRecord { return read.withoutClaims(hostnames) })
	return err
}

func (e *Edge) applyTrust(ctx context.Context, tier environment.Tier, change func(trustRecord) trustRecord) (Front, trustRecord, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, e.frontTarget(tier))
	if err != nil {
		return Front{}, trustRecord{}, err
	}
	front := frontOf(outputs)
	front.Shielded = true
	record, err := e.changeTrust(ctx, tier, change)
	if err != nil {
		return Front{}, trustRecord{}, err
	}
	allowlist := record.allowlist()
	if len(allowlist) > maxAllowlisted {
		return Front{}, trustRecord{}, fmt.Errorf("the %s front of tier %s would trust %d client certificates, and a Certificate Manager trust config allowlists at most %d: delete the client certificates your Cloudflare zones no longer present, and deploy again",
			Kind, tier, len(allowlist), maxAllowlisted)
	}
	if !front.provisioned() || slices.Equal(record.Provisioned, allowlist) {
		return front, record, nil
	}
	front, err = e.raiseTrusting(ctx, tier, allowlist)
	return front, record, err
}

func (e *Edge) raiseTrusting(ctx context.Context, tier environment.Tier, allowlist []string) (Front, error) {
	front, err := e.raise(ctx, tier, progress.DiscardProgress())
	if err != nil {
		return Front{}, err
	}
	_, err = e.changeTrust(ctx, tier, func(read trustRecord) trustRecord {
		if slices.Equal(read.allowlist(), allowlist) {
			read.Provisioned = allowlist
		}
		return read
	})
	return front, err
}

func (e *Edge) frontFor(claim router.Claim) *Edge {
	if len(claim.ClientCertificates) > 0 {
		return e.Shielded()
	}
	return e
}

func (e *Edge) readProvisionedFront(ctx context.Context, tier environment.Tier) (Front, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, e.frontTarget(tier))
	if err != nil {
		return Front{}, err
	}
	front := frontOf(outputs)
	if !front.provisioned() {
		return Front{}, refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s: run `ocel bootstrap` for this tier first", Kind, tier)
	}
	return front, nil
}

func mintUnpresentableCertificate(now time.Time) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate a placeholder client certificate: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ocel: no edge presents this"},
		DNSNames:     []string{"unpresentable.invalid"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", fmt.Errorf("sign a placeholder client certificate: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}
