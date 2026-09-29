package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/edge"
)

type Proxy struct{ p *cloudflare }

func NewProxy(namespace string) *Proxy { return &Proxy{p: newCloudflare(namespace)} }

func (x *Proxy) Kind() edge.Kind { return Kind }

func (x *Proxy) Facts() edge.Facts {
	return edge.Facts{
		Supported:         []edge.Need{edge.NeedStreaming},
		ProxiesRecords:    true,
		ProxiedRecordNote: "Turn the Cloudflare proxy (orange cloud) on for these records: Cloudflare forwards each hostname to the value it points at.",
		ShieldsOrigin:     true,
		CredentialScope:   readAccountID(),
	}
}

func (x *Proxy) Hooks() edge.Hooks {
	return edge.Hooks{
		VerifyCredentials:             x.p.verifyCredentials,
		DescribeCredentialPermissions: proxyCredentialPermissions,
		ClientCertificates: &edge.ClientCertificateHooks{
			Ensure:  x.p.ensureClientCertificates,
			Present: x.p.presentClientCertificates,
		},
		OriginCertificates: &edge.OriginCertificateHooks{
			Issue:  x.p.issueOriginCertificate,
			Revoke: x.p.revokeOriginCertificate,
		},
		PurgeHostnames: x.p.purgeHostnames,
	}
}
