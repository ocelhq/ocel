package alb

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	maxResourceName = 63
	bindingPurpose  = "binding"
	previewScope    = "wildcard"
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
	GatePlugin     string
	GateExtension  string
}

func loadBalancerNames(namespace string, tier environment.Tier) names {
	stem := naming.OcelName(namespace, tier, string(Kind))
	return names{
		Address:        stem + "-address",
		NotFound:       stem + "-notfound",
		AssetsBackend:  stem + "-assets",
		AssetsReader:   stem + "-reader",
		CertificateMap: stem + "-certs",
		URLMap:         stem + "-routes",
		Proxy:          stem + "-https",
		Rule:           stem + "-forward",
		GatePlugin:     stem + "-gate",
		GateExtension:  stem + "-gate-edge",
	}
}

func hostResourceName(namespace string, tier environment.Tier, purpose, scope, hostname string, role ...string) (string, error) {
	name, err := naming.TaggedName{
		Head:     naming.OcelName(namespace, tier, string(Kind)),
		Readable: naming.Join(naming.WordSeparator, scope, naming.SanitizeHost(hostname)),
		Tag:      naming.HashTag(namespace, string(tier), purpose, scope, hostname),
	}.Fit(maxResourceName, certRole, negRole)
	if err != nil {
		return "", err
	}
	return naming.Join(naming.WordSeparator, append([]string{name}, role...)...), nil
}

func backendName(namespace, slug string, tier environment.Tier, hostname string) (string, error) {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname)
}

func entryName(namespace, slug string, tier environment.Tier, hostname string) (string, error) {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname, certRole)
}

func negName(namespace, slug string, tier environment.Tier, hostname string) (string, error) {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname, negRole)
}

func previewEntryName(namespace, baseDomain string) (string, error) {
	return hostResourceName(namespace, environment.TierPreview, previewScope, previewScope, baseDomain, certRole)
}

func Surface(namespace, slug string, tier environment.Tier) string {
	return naming.Join(naming.FieldSeparator, naming.NamespaceField(namespace), string(Kind), slug, naming.TierWord(tier))
}
