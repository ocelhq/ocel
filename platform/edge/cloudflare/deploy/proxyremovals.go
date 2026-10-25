package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	kindDNSRecord             = "Cloudflare::DNSRecord"
	kindOriginPullAssociation = "Cloudflare::OriginPullAssociation"
	kindOriginPullCertificate = "Cloudflare::OriginPullCertificate"
)

func (x *Proxy) PlanProjectRemoval(scope edge.ProjectScope) ([]edge.PlanGroup, error) {
	group := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
	}
	for _, hostname := range scope.Hostnames {
		group.Changes = append(group.Changes, edge.PlanChange{
			Kind: kindDNSRecord, Name: hostname, Action: edge.PlanDelete,
		}, edge.PlanChange{
			Kind: kindOriginPullAssociation, Name: hostname, Action: edge.PlanDelete,
		})
	}
	if len(group.Changes) == 0 {
		group.Action = edge.PlanKeep
		group.Reason = "no hostname forwarded"
	}
	return []edge.PlanGroup{group, {
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind) + "/zone",
		Action: edge.PlanKeep,
		Reason: "goes with the tier's bootstrap",
		Changes: []edge.PlanChange{{
			Kind: kindOriginPullCertificate, Name: "this origin's client certificate in each zone", Action: edge.PlanKeep,
		}},
	}}, nil
}

func (x *Proxy) PlanPreviewWildcardRemoval(wildcard string) (removed, kept edge.PlanGroup, err error) {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind: kindDNSRecord, Name: wildcard, Action: edge.PlanDelete,
		}},
	}, x.PlanSharedPreviewRemoval(), nil
}

func (x *Proxy) PlanSharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "nothing shared",
	}
}
