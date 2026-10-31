package albresources

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

func BackendName(namespace, slug string, tier environment.Tier, hostname string) (string, error) {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname)
}

func EntryName(namespace, slug string, tier environment.Tier, hostname string) (string, error) {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname, certRole)
}

func NegName(namespace, slug string, tier environment.Tier, hostname string) (string, error) {
	return hostResourceName(namespace, tier, bindingPurpose, slug, hostname, negRole)
}

func PreviewEntryName(namespace, baseDomain string) (string, error) {
	return hostResourceName(namespace, environment.TierPreview, previewScope, previewScope, baseDomain, certRole)
}
