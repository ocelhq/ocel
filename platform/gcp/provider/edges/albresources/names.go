package albresources

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

type Names struct {
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

func LoadBalancerNames(namespace string, tier environment.Tier) Names {
	stem := naming.OcelName(namespace, tier, string(Kind))
	return Names{
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

func URLMapPath(namespace, project string, tier environment.Tier) string {
	return "projects/" + project + "/global/urlMaps/" + LoadBalancerNames(namespace, tier).URLMap
}
