package gcp

import (
	"context"
	"path"

	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func (p *Provider) ListProjectResources(_ context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	var listed []provider.PhysicalResource
	for _, cert := range req.Certificates {
		if cert.Requested && cert.Issued() {
			listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindCertificate), ID: path.Base(cert.ID), Location: bootstrap.LocationGlobal})
		}
	}
	if req.EdgeKind == alb.Kind {
		hosts, err := alb.ReadHostResources(string(p.namespace), req.EdgeStack)
		if err != nil {
			return nil, err
		}
		for _, host := range hosts {
			listed = append(listed, p.albHostResources(host)...)
		}
	}
	return append(listed, cloudflare.ListProjectResources(string(p.namespace), req)...), nil
}

func (p *Provider) albHostResources(host alb.HostResources) []provider.PhysicalResource {
	var listed []provider.PhysicalResource
	if host.CertificateMapEntry != "" {
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindCertificateMapEntry), ID: host.CertificateMapEntry, Location: bootstrap.LocationGlobal})
	}
	if host.NetworkEndpointGroup != "" {
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindNetworkEndpointGroup), ID: host.NetworkEndpointGroup, Location: p.options.Region})
	}
	if host.BackendService != "" {
		listed = append(listed, provider.PhysicalResource{Kind: string(bootstrap.KindBackendService), ID: host.BackendService, Location: bootstrap.LocationGlobal})
	}
	return listed
}
