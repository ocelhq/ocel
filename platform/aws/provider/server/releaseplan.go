package server

import (
	"fmt"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func refusePreviewRelease(baseDomain string, projects []string) error {
	if len(projects) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s still carries live preview pointers for %d project(s): %s — nothing would serve them the moment it is released. Remove one preview with `ocel preview rm`, or a project's whole preview footprint with `ocel destroy --preview`, in each of them first",
		edge.PreviewWildcard(baseDomain), len(projects), strings.Join(projects, ", "),
	)
}

func releaseEdgeStackPlan(recorded bootstrap.PreviewDomain) (*deploymentsv1.EdgeStackPlan, error) {
	kind, err := previewWildcardHolder(recorded)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.EdgeStackPlan{
		EdgeKind: string(kind),
		Items:    releasePlanItems(kind, recorded),
	}, nil
}

func releasePlanItems(kind edge.Kind, recorded bootstrap.PreviewDomain) []*deploymentsv1.TeardownItem {
	wildcard := edge.PreviewWildcard(recorded.BaseDomain)

	items := []*deploymentsv1.TeardownItem{wildcardItem(kind, wildcard)}
	if arn := recorded.Certificate.ARN; arn != "" {
		items = append(items, certificateItem(recorded.Certificate))
	}
	for _, rec := range recorded.Records {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range recorded.Owed {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	return append(items, releaseKeptItem(kind))
}

func releaseKeptItem(kind edge.Kind) *deploymentsv1.TeardownItem {
	if kind == cloudflare.Kind {
		return &deploymentsv1.TeardownItem{
			Kind:   "preview substrate",
			Name:   bootstrap.ClassPreview,
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "substrate-scoped: `ocel bootstrap --destroy --preview` removes what it stood up",
		}
	}
	return sharedEdgeItem(kind)
}

func wildcardItem(kind edge.Kind, wildcard string) *deploymentsv1.TeardownItem {
	switch kind {
	case cloudflare.Kind:
		return &deploymentsv1.TeardownItem{
			Kind:   "preview entry worker",
			Name:   wildcard,
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "the shared entry worker holding this wildcard, and the route that reaches it",
		}
	case cloudfront.Kind:
		return &deploymentsv1.TeardownItem{
			Kind:   "wildcard distribution",
			Name:   wildcard,
			Action: deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE,
			Reason: "the CloudFront distribution every project's previews are served through; CloudFront only deletes a disabled distribution once the disable has reached every edge",
			Slow:   true,
		}
	default:
		return &deploymentsv1.TeardownItem{
			Kind:   "wildcard domain name",
			Name:   wildcard,
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "the API Gateway domain name every project's previews are routed through, and the rules under it",
		}
	}
}
