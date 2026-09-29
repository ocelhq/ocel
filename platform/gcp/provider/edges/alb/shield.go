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
	"math/big"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const shieldedWord = "shielded"

const trustAttempts = 8

func (e *Edge) Shielded() *Edge {
	deps := e.deps
	deps.Shielded = true
	return New(deps)
}

type trustRecord struct {
	Lineages [][]string `json:"lineages,omitempty"`
	Raised   []string   `json:"raised,omitempty"`
}

func (r trustRecord) trusted() []string {
	var trusted []string
	for _, lineage := range r.Lineages {
		trusted = append(trusted, lineage...)
	}
	slices.Sort(trusted)
	return slices.Compact(trusted)
}

func (r trustRecord) succeededBy(certificates []string, adding bool) (trustRecord, bool) {
	lineage := slices.Compact(slices.Sorted(slices.Values(certificates)))
	kept := make([][]string, 0, len(r.Lineages)+1)
	replaced := false
	for _, held := range r.Lineages {
		if slices.ContainsFunc(held, func(certificate string) bool { return slices.Contains(lineage, certificate) }) {
			replaced = true
			continue
		}
		kept = append(kept, held)
	}
	if !replaced && !adding {
		return r, false
	}
	kept = append(kept, lineage)
	slices.SortFunc(kept, slices.Compare)
	kept = slices.CompactFunc(kept, slices.Equal)
	if slices.EqualFunc(kept, r.Lineages, slices.Equal) {
		return r, false
	}
	r.Lineages = kept
	return r, true
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

func (e *Edge) changeTrust(ctx context.Context, tier environment.Tier, change func(trustRecord) (trustRecord, bool)) (trustRecord, error) {
	for range trustAttempts {
		entry, read, err := e.readTrust(ctx, tier)
		if err != nil {
			return trustRecord{}, err
		}
		changed, write := change(read)
		if !write {
			return changed, nil
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

func (e *Edge) ensureTrusted(ctx context.Context, tier environment.Tier) ([]string, error) {
	record, err := e.changeTrust(ctx, tier, func(read trustRecord) (trustRecord, bool) {
		return read, false
	})
	if err != nil {
		return nil, err
	}
	if len(record.Lineages) > 0 {
		return record.trusted(), nil
	}
	placeholder, err := mintUnpresentableCertificate(time.Now())
	if err != nil {
		return nil, err
	}
	record, err = e.changeTrust(ctx, tier, func(read trustRecord) (trustRecord, bool) {
		if len(read.Lineages) > 0 {
			return read, false
		}
		read.Lineages = [][]string{{placeholder}}
		return read, true
	})
	return record.trusted(), err
}

func (e *Edge) shield(ctx context.Context, tier environment.Tier, certificates []string) (Front, error) {
	front, err := e.trust(ctx, tier, certificates, true)
	if err != nil {
		return Front{}, err
	}
	if _, err := e.trust(ctx, tier.Sibling(), certificates, false); err != nil {
		return Front{}, err
	}
	return front, nil
}

func (e *Edge) trust(ctx context.Context, tier environment.Tier, certificates []string, adding bool) (Front, error) {
	outputs, err := e.deps.Stacks.Outputs(ctx, e.frontTarget(tier))
	if err != nil {
		return Front{}, err
	}
	front := frontOf(outputs)
	front.Shielded = true
	if !adding && !front.provisioned() {
		return front, nil
	}
	record, err := e.changeTrust(ctx, tier, func(read trustRecord) (trustRecord, bool) {
		return read.succeededBy(certificates, adding)
	})
	if err != nil {
		return Front{}, err
	}
	trusted := record.trusted()
	if front.provisioned() && slices.Equal(record.Raised, trusted) {
		return front, nil
	}
	front, err = e.raise(ctx, tier, progress.DiscardProgress())
	if err != nil {
		return Front{}, err
	}
	_, err = e.changeTrust(ctx, tier, func(read trustRecord) (trustRecord, bool) {
		if !slices.Equal(read.trusted(), trusted) {
			return read, false
		}
		read.Raised = trusted
		return read, true
	})
	return front, err
}

func (e *Edge) ensureFront(ctx context.Context, tier environment.Tier, certificates []string) (Front, error) {
	if len(certificates) > 0 {
		return e.shield(ctx, tier, certificates)
	}
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
