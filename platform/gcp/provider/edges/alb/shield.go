package alb

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
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

func (r trustRecord) recordClaim(hostname string, certificates []string) trustRecord {
	r = r.addToHostnamesSharing(certificates)
	r.Hostnames[hostname] = sortedCertificates(certificates)
	return r
}

func (r trustRecord) addToHostnamesSharing(certificates []string) trustRecord {
	hostnames := make(map[string][]string, len(r.Hostnames)+1)
	for hostname, held := range r.Hostnames {
		if slices.ContainsFunc(held, func(certificate string) bool { return slices.Contains(certificates, certificate) }) {
			held = sortedCertificates(slices.Concat(held, certificates))
		}
		hostnames[hostname] = held
	}
	r.Hostnames = hostnames
	return r
}

func (r trustRecord) removeClaims(hostnames []string) trustRecord {
	r.Hostnames = maps.Clone(r.Hostnames)
	for _, hostname := range hostnames {
		delete(r.Hostnames, hostname)
	}
	return r
}

func (r trustRecord) refuseOverAllowlisted(tier environment.Tier) error {
	if allowlisted := len(r.allowlist()); allowlisted > maxAllowlisted {
		return refusal.Refuse(refusal.CodeInvalid,
			"the %s load balancer of tier %s would trust %d client certificates, and a Certificate Manager trust config allowlists at most %d: delete the client certificates your Cloudflare zones no longer present, and deploy again",
			Kind, tier, allowlisted, maxAllowlisted)
	}
	return nil
}

func fingerprintAllowlist(allowlist []string) string {
	sum := sha256.New()
	for _, certificate := range sortedCertificates(allowlist) {
		sum.Write([]byte(certificate))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
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
		return keyvalue.Entry{}, trustRecord{}, fmt.Errorf("read which client certificates the %s load balancer of tier %s trusts: %w", Kind, tier, err)
	}
	var read trustRecord
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &read); err != nil {
			return keyvalue.Entry{}, trustRecord{}, fmt.Errorf("decode which client certificates the %s load balancer of tier %s trusts: %w", Kind, tier, err)
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
		if err := changed.refuseOverAllowlisted(tier); err != nil {
			return trustRecord{}, err
		}
		if entry.Value, err = json.Marshal(changed); err != nil {
			return trustRecord{}, err
		}
		_, err = e.deps.KeyValues.Write(ctx, entry)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return trustRecord{}, fmt.Errorf("record which client certificates the %s load balancer of tier %s trusts: %w", Kind, tier, err)
		}
		return changed, nil
	}
	return trustRecord{}, fmt.Errorf("record which client certificates the %s load balancer of tier %s trusts: it changed under every one of %d attempts", Kind, tier, trustAttempts)
}

func (e *Edge) forgetTrust(ctx context.Context, tier environment.Tier) error {
	entry, err := e.deps.KeyValues.Read(ctx, e.trustKey(tier))
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil
	}
	if err == nil {
		err = e.deps.KeyValues.Remove(ctx, entry.Key, entry.Revision)
	}
	if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
		return fmt.Errorf("forget which client certificates the %s load balancer of tier %s trusted: %w", Kind, tier, err)
	}
	return nil
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

func (e *Edge) trustClaim(ctx context.Context, tier environment.Tier, hostname string, certificates []string) (LoadBalancer, error) {
	sharing := func(read trustRecord) trustRecord { return read.addToHostnamesSharing(certificates) }
	_, sibling, err := e.readTrust(ctx, tier.Sibling())
	if err != nil {
		return LoadBalancer{}, err
	}
	if err := sharing(sibling).refuseOverAllowlisted(tier.Sibling()); err != nil {
		return LoadBalancer{}, err
	}
	balancer, err := e.applyTrust(ctx, tier, func(read trustRecord) trustRecord { return read.recordClaim(hostname, certificates) })
	if err != nil {
		return LoadBalancer{}, err
	}
	if !balancer.provisioned() {
		if balancer, err = e.raiseTrusting(ctx, tier); err != nil {
			return LoadBalancer{}, err
		}
	}
	if _, err := e.applyTrust(ctx, tier.Sibling(), sharing); err != nil {
		return LoadBalancer{}, err
	}
	return balancer, nil
}

func (e *Edge) withdrawClaims(ctx context.Context, tier environment.Tier, hostnames ...string) error {
	_, err := e.applyTrust(ctx, tier, func(read trustRecord) trustRecord { return read.removeClaims(hostnames) })
	return err
}

func (e *Edge) applyTrust(ctx context.Context, tier environment.Tier, change func(trustRecord) trustRecord) (LoadBalancer, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(tier))
	if err != nil {
		return LoadBalancer{}, err
	}
	balancer := loadBalancerOf(outputs)
	balancer.Shielded = true
	record, err := e.changeTrust(ctx, tier, change)
	if err != nil {
		return LoadBalancer{}, err
	}
	if !balancer.provisioned() || balancer.AllowlistFingerprint == fingerprintAllowlist(record.allowlist()) {
		return balancer, nil
	}
	return e.raiseTrusting(ctx, tier)
}

func (e *Edge) raiseTrusting(ctx context.Context, tier environment.Tier) (LoadBalancer, error) {
	for range trustAttempts {
		balancer, err := e.raise(ctx, tier, progress.DiscardProgress())
		if err != nil {
			return LoadBalancer{}, err
		}
		_, record, err := e.readTrust(ctx, tier)
		if err != nil {
			return LoadBalancer{}, err
		}
		if balancer.AllowlistFingerprint == fingerprintAllowlist(record.allowlist()) {
			return balancer, nil
		}
	}
	return LoadBalancer{}, fmt.Errorf("raise the %s load balancer of tier %s trusting the client certificates recorded for it: they changed under every one of %d raises", Kind, tier, trustAttempts)
}

func (e *Edge) loadBalancerFor(claim router.Claim) *Edge {
	if len(claim.ClientCertificates) > 0 {
		return e.Shielded()
	}
	return e
}

func (e *Edge) readProvisionedLoadBalancer(ctx context.Context, tier environment.Tier) (LoadBalancer, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(tier))
	if err != nil {
		return LoadBalancer{}, err
	}
	balancer := loadBalancerOf(outputs)
	if !balancer.provisioned() {
		return LoadBalancer{}, refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s: run `ocel bootstrap` for this tier first", Kind, tier)
	}
	return balancer, nil
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
