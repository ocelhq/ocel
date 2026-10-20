package aws

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/apigateway"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var edgeResources = map[string]func(state edge.StackState, region string) ([]provider.PhysicalResource, error){
	string(alb.Kind):        alb.ListProjectResources,
	string(apigateway.Kind): apigateway.ListProjectResources,
}

func (p *Provider) ListProjectResources(ctx context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	return listProjectResources(ctx, p.KeyValues(), p.namespace, p.aws.Region, req)
}

func listProjectResources(ctx context.Context, store records.Store, ns account.Namespace, region string, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	routed, err := listEdgeResources(region, req)
	if err != nil {
		return nil, err
	}
	distributions, err := cloudfront.ReadProjectResources(ctx, store, ns, req.Project, req.Tier)
	if err != nil {
		return nil, err
	}
	listed := slices.Concat(listImageRepositories(string(ns), req), routed, distributions, listRequestedCertificates(req), listRoute53Records(req), cloudflare.ListProjectResources(string(ns), req))
	seen := map[provider.PhysicalResource]bool{}
	return slices.DeleteFunc(listed, func(resource provider.PhysicalResource) bool {
		named := seen[resource]
		seen[resource] = true
		return named
	}), nil
}

func listImageRepositories(namespace string, req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	project := naming.Sanitize(req.Project)
	var repositories []provider.PhysicalResource
	for _, image := range req.Images {
		name, region, found := registry.ParseImageRepository(namespace, req.Tier, image)
		if !found {
			continue
		}
		if owner, _ := images.RegistryRepositoryProject(strings.TrimPrefix(name, registry.RepositoryPrefix(namespace, req.Tier)+naming.PathSeparator)); owner == project {
			repositories = append(repositories, cfn.NewPhysicalResource("AWS::ECR::Repository", name, region))
		}
	}
	return repositories
}

func listEdgeResources(region string, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	served := map[string]edge.StackState{string(req.EdgeKind): req.EdgeStack}
	for kind, routed := range req.Routers {
		served[string(kind)] = routed.Edge
	}
	var resources []provider.PhysicalResource
	for _, kind := range slices.Sorted(maps.Keys(served)) {
		list, known := edgeResources[kind]
		if !known {
			continue
		}
		listed, err := list(served[kind], region)
		if err != nil {
			return nil, fmt.Errorf("read what the %s edge of %s holds: %w", kind, req.Project, err)
		}
		resources = append(resources, listed...)
	}
	return resources, nil
}

func listRequestedCertificates(req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	var requested []provider.PhysicalResource
	for _, cert := range req.Certificates {
		if cert.Requested && cert.ID != "" {
			requested = append(requested, cfn.NewPhysicalResource("AWS::CertificateManager::Certificate", cert.ID, certs.RegionOfARN(cert.ID)))
		}
	}
	return requested
}

func listRoute53Records(req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	if req.DNSKind != provider.DNSKind(dns.KindRoute53) {
		return nil
	}
	written := make([]provider.PhysicalResource, 0, len(req.WrittenRecords))
	for _, record := range req.WrittenRecords {
		written = append(written, cfn.NewPhysicalResource("AWS::Route53::RecordSet", record.String(), ""))
	}
	return written
}
