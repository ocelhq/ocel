package alb

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	maxResourceName = 63
	bindingPurpose  = "binding"
	previewScope    = "wildcard"
	originScope     = "origin"
	certRole        = "cert"
	negRole         = "neg"
)

type names struct {
	Address        string
	NotFound       string
	AssetsBackend  string
	AssetsReader   string
	CertificateMap string
	URLMap         string
	Proxy          string
	Rule           string
	Trust          string
	Policy         string
	RedirectURLMap string
	HTTPProxy      string
	HTTPRule       string
	GatePlugin     string
	GateExtension  string
}

func loadBalancerNames(namespace string, tier environment.Tier, shielded bool) names {
	words := []string{string(Kind)}
	if shielded {
		words = append(words, shieldedNameSegment)
	}
	stem := naming.OcelName(namespace, tier, words...)
	return names{
		Address:        stem + "-address",
		NotFound:       stem + "-notfound",
		AssetsBackend:  stem + "-assets",
		AssetsReader:   stem + "-reader",
		CertificateMap: stem + "-certs",
		URLMap:         stem + "-routes",
		Proxy:          stem + "-https",
		Rule:           stem + "-forward",
		Trust:          stem + "-trust",
		Policy:         stem + "-mtls",
		RedirectURLMap: stem + "-redirect",
		HTTPProxy:      stem + "-http",
		HTTPRule:       stem + "-forward-http",
		GatePlugin:     stem + "-gate",
		GateExtension:  stem + "-gate-edge",
	}
}

func hostResourceName(namespace string, tier environment.Tier, purpose, scope, hostname string, role ...string) string {
	name := naming.TaggedName{
		Head:     naming.OcelName(namespace, tier, string(Kind)),
		Readable: naming.Join(naming.WordSeparator, scope, naming.SanitizeHost(hostname)),
		Tag:      naming.HashTag(namespace, string(tier), purpose, scope, hostname),
	}.Fit(maxResourceName, certRole, negRole)
	return naming.Join(naming.WordSeparator, append([]string{name}, role...)...)
}

func backendName(namespace, slug string, tier environment.Tier, hostname string) string {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname)
}

func entryName(namespace, slug string, tier environment.Tier, hostname string) string {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname, certRole)
}

func negName(namespace, slug string, tier environment.Tier, hostname string) string {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname, negRole)
}

func previewEntryName(namespace, baseDomain string) string {
	return hostResourceName(namespace, environment.TierPreview, previewScope, previewScope, baseDomain, certRole)
}

func originEntryName(namespace string, tier environment.Tier, baseDomain string) string {
	return hostResourceName(namespace, tier, originScope, originScope, baseDomain, certRole)
}

func Surface(namespace, slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, naming.NamespaceField(namespace), string(Kind), slug, naming.TierWord(tier))
}
