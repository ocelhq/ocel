package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
)

type Proxy struct{ p *cloudflare }

func NewProxy(namespace string, origin func(context.Context) (OriginScope, error), options Options) *Proxy {
	return &Proxy{p: newCloudflare(namespace, origin, options)}
}

func (x *Proxy) Kind() edge.Kind { return Kind }

func (x *Proxy) Facts() edge.Facts {
	return edge.Facts{
		Supported:          []edge.Need{edge.NeedStreaming, edge.NeedRevalidation},
		ProxiesRecords:     true,
		ProxiedRecordNote:  "Turn the Cloudflare proxy (orange cloud) on for these records: Cloudflare forwards each hostname to the value it points at.",
		ShieldsOrigin:      true,
		OriginFacingRanges: listOriginFacingRanges(),
		TunnelsToOrigin:    x.p.options.Tunnel,
		CredentialScope:    readAccountID(),
	}
}

func (x *Proxy) Hooks() edge.Hooks {
	return edge.Hooks{
		VerifyCredentials:             x.p.verifyCredentials,
		DescribeCredentialPermissions: proxyCredentialPermissions,
		ClientCertificates: &edge.ClientCertificateHooks{
			RefuseUnready: x.p.refuseUnreadyClientCertificates,
			Ensure:        x.p.ensureClientCertificates,
			Present:       x.p.presentClientCertificates,
		},
		OriginCertificates: &edge.OriginCertificateHooks{
			Issue:  x.p.issueOriginCertificate,
			Revoke: x.p.revokeOriginCertificate,
		},
		PurgeHostnames: x.p.purgeHostnames,
		Tunnels: &edge.TunnelHooks{
			Connector: newCloudflaredConnector(),
			Ensure:    x.p.ensureTunnel,
			Configure: x.p.configureTunnel,
			ReadToken: x.p.readTunnelToken,
			Delete:    x.p.deleteTunnel,
		},
	}
}
