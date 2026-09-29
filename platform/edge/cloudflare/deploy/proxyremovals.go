package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	kindDNSRecord             = "Cloudflare::DNSRecord"
	kindOriginPullCertificate = "Cloudflare::OriginPullCertificate"
)

func (x *Proxy) ProjectRemovals(scope edge.ProjectScope) []edge.PlanGroup {
	group := edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
	}
	for _, hostname := range scope.Hostnames {
		group.Changes = append(group.Changes, edge.PlanChange{
			Kind: kindDNSRecord, Name: hostname, Action: edge.PlanDelete,
			Reason: "the proxied record that forwards it to the origin",
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
		Reason: "the client certificate each zone presents to origins is shared by every project forwarded through that zone",
		Changes: []edge.PlanChange{{
			Kind: kindOriginPullCertificate, Name: "zone-level authenticated origin pulls", Action: edge.PlanKeep,
		}},
	}}
}

func (x *Proxy) PreviewWildcardRemovals(wildcard string) (removed, kept edge.PlanGroup) {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanDelete,
		Changes: []edge.PlanChange{{
			Kind: kindDNSRecord, Name: wildcard, Action: edge.PlanDelete,
			Reason: "the proxied record that would forward it to the origin",
		}},
	}, x.SharedPreviewRemoval()
}

func (x *Proxy) SharedPreviewRemoval() edge.PlanGroup {
	return edge.PlanGroup{
		Kind:   edge.EdgeGroupKind,
		Name:   edge.EdgeGroupName(Kind),
		Action: edge.PlanKeep,
		Reason: "the proxy provisions nothing shared by previews",
	}
}
