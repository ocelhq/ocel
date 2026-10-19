package cloudflare

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	maxOriginDomain  = 181
	maxDNSLabel      = 63
	originDomainPath = "edge.cloudflare.originDomain"
	previewLabel     = "preview."
)

type Options struct {
	Tunnel       bool   `json:"tunnel,omitempty" doc:"Reach the origin through a tunnel it opens to Cloudflare, instead of at its address. A VPS always does."`
	OriginDomain string `json:"originDomain,omitempty" doc:"A domain in your Cloudflare zone, used by nothing else, that the worker reaches Google Cloud apps under. AWS refuses it."`
}

func (Options) Doc() string {
	return "Options for the Cloudflare edge. The token and account id are read from the environment."
}

func (o Options) OriginBase(tier environment.Tier) string {
	if o.OriginDomain == "" {
		return ""
	}
	if tier == environment.TierPreview {
		return previewLabel + o.OriginDomain
	}
	return o.OriginDomain
}

func DecodeOptions(options provider.Options) (Options, error) {
	decoded, err := provider.DecodeEdgeOptions[Options](Kind, options)
	if err != nil {
		return Options{}, err
	}
	if decoded.OriginDomain == "" {
		return decoded, nil
	}
	if decoded.OriginDomain, err = originDomain(decoded.OriginDomain); err != nil {
		return Options{}, err
	}
	return decoded, nil
}

func originDomain(raw string) (string, error) {
	domain := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	if wildcarded, found := strings.CutPrefix(domain, "*."); found {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s is the domain itself, not a wildcard: every deployment is served on its own subdomain of it, so pass %q", originDomainPath, wildcarded)
	}
	labels := strings.Split(domain, ".")
	malformed := strings.ContainsAny(domain, "/:* ") || len(labels) < 2
	for _, label := range labels {
		malformed = malformed || label == "" || len(label) > maxDNSLabel
	}
	if malformed {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s %q is not a domain name: pass a hostname like origin.acme.com whose labels are 1 to %d characters", originDomainPath, raw, maxDNSLabel)
	}
	if len(domain) > maxOriginDomain {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s is %d characters and may be %d: the deployment hostnames under it would pass the 253-character DNS limit", originDomainPath, len(domain), maxOriginDomain)
	}
	return domain, nil
}
