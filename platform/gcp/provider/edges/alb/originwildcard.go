package alb

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"slices"
	"time"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type originWildcardEntry struct {
	BaseDomain  string `json:"baseDomain"`
	Certificate string `json:"certificate"`
}

type OriginWildcardSpec struct {
	Tier        environment.Tier
	BaseDomain  string
	Certificate string
	ClientCAs   []string
}

func (e *Edge) originWildcardKey(tier environment.Tier) keyvalue.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), "origin-wildcard")
}

func (e *Edge) recordedOriginWildcard(ctx context.Context, tier environment.Tier) (originWildcardEntry, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.originWildcardKey(tier))
	if err != nil {
		return originWildcardEntry{}, fmt.Errorf("read which origin wildcard the %s load balancer of tier %s serves: %w", Kind, tier, err)
	}
	var recorded originWildcardEntry
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return originWildcardEntry{}, fmt.Errorf("decode which origin wildcard the %s load balancer of tier %s serves: %w", Kind, tier, err)
	}
	return recorded, nil
}

func (e *Edge) rememberOriginWildcard(ctx context.Context, tier environment.Tier, recorded originWildcardEntry) error {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, e.originWildcardKey(tier))
	if err != nil {
		return fmt.Errorf("read which origin wildcard the %s load balancer of tier %s serves: %w", Kind, tier, err)
	}
	if entry.Value, err = json.Marshal(recorded); err != nil {
		return fmt.Errorf("encode which origin wildcard the %s load balancer of tier %s serves: %w", Kind, tier, err)
	}
	if _, err := e.deps.KeyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record which origin wildcard the %s load balancer of tier %s serves: %w", Kind, tier, err)
	}
	return nil
}

func (e *Edge) ReconcileOriginWildcard(ctx context.Context, spec OriginWildcardSpec) (string, error) {
	if !e.deps.Shielded {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"only the shielded %s load balancer answers an origin wildcard: it is the one that checks the worker's client certificate", Kind)
	}
	if spec.BaseDomain == "" || spec.Certificate == "" || len(spec.ClientCAs) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the %s load balancer answers an origin wildcard on its own certificate for the worker's client CA, and this reconcile names a base domain %q, a certificate %q and %d client CAs",
			Kind, spec.BaseDomain, spec.Certificate, len(spec.ClientCAs))
	}
	recorded, err := e.recordedOriginWildcard(ctx, spec.Tier)
	if err != nil {
		return "", err
	}
	if recorded.BaseDomain != "" && recorded.BaseDomain != spec.BaseDomain {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"the %s load balancer of tier %s already answers the origin wildcard of %s, and a tier has one: this reconcile names %s",
			Kind, spec.Tier, recorded.BaseDomain, spec.BaseDomain)
	}
	entry := originWildcardEntry{BaseDomain: spec.BaseDomain, Certificate: spec.Certificate}
	isNew := recorded != entry
	if isNew {
		if err := e.rememberOriginWildcard(ctx, spec.Tier, entry); err != nil {
			return "", err
		}
	}
	hostname := "*." + spec.BaseDomain
	_, before, err := e.readTrust(ctx, spec.Tier)
	if err != nil {
		return "", err
	}
	trusted, err := stillValidCAs(before.Hostnames[hostname], spec.ClientCAs, time.Now())
	if err != nil {
		return "", err
	}
	balancer, err := e.trustClaim(ctx, spec.Tier, hostname, trusted)
	if err != nil {
		return "", err
	}
	_, after, err := e.readTrust(ctx, spec.Tier)
	if err != nil {
		return "", err
	}
	if isNew && fingerprintTrusted(before.listTrusted()) == fingerprintTrusted(after.listTrusted()) {
		if balancer, err = e.raise(ctx, spec.Tier, progress.Discard()); err != nil {
			return "", err
		}
	}
	return balancer.Address, nil
}

func stillValidCAs(held, current []string, now time.Time) ([]string, error) {
	kept := slices.Clone(current)
	for _, certificate := range held {
		block, _ := pem.Decode([]byte(certificate))
		if block == nil {
			return nil, fmt.Errorf("a client CA recorded for the origin wildcard is no PEM certificate")
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("read a client CA recorded for the origin wildcard: %w", err)
		}
		if parsed.NotAfter.After(now) {
			kept = append(kept, certificate)
		}
	}
	return sortedCertificates(kept), nil
}

func (e *Edge) DestroyOriginWildcard(ctx context.Context, tier environment.Tier) error {
	recorded, err := e.recordedOriginWildcard(ctx, tier)
	if err != nil || recorded.BaseDomain == "" {
		return err
	}
	if err := keyvalue.Forget(ctx, e.deps.KeyValues, e.originWildcardKey(tier)); err != nil {
		return fmt.Errorf("release which origin wildcard the %s load balancer of tier %s served: %w", Kind, tier, err)
	}
	outputs, err := e.deps.Stacks.Outputs(ctx, e.loadBalancerTarget(tier))
	if err != nil {
		return err
	}
	if loadBalancerOf(outputs).provisioned() {
		if _, err := e.raise(ctx, tier, progress.Discard()); err != nil {
			return err
		}
	}
	return e.withdrawClaims(ctx, tier, "*."+recorded.BaseDomain)
}

func originEntryName(tier environment.Tier, baseDomain string) string {
	return naming.Fit(maxResourceName, naming.WordSeparator,
		naming.Fixed("ocel"),
		naming.Fixed(string(Kind)),
		naming.Fixed("origin"),
		naming.Fixed(string(tier)),
		naming.Compressible(naming.SanitizeHost(baseDomain)),
		naming.Fixed("cert"),
	)
}

func originWildcardResources(ctx *pulumi.Context, spec loadBalancerSpec, project string) error {
	base := spec.Origin.BaseDomain
	if base == "" {
		return nil
	}
	entry := originEntryName(spec.Tier, base)
	_, err := certificatemanager.NewCertificateMapEntry(ctx, entry, &certificatemanager.CertificateMapEntryArgs{
		Name:         pulumi.String(entry),
		Project:      pulumi.String(project),
		Map:          pulumi.String(spec.Names.CertificateMap),
		Hostname:     pulumi.String("*." + base),
		Certificates: pulumi.StringArray{pulumi.String(spec.Origin.Certificate)},
	})
	return err
}
