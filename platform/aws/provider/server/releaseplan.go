package server

import (
	"fmt"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
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

func releaseEdgeStackPlan(edgeFront edge.Edge, recorded bootstrap.PreviewDomain) *deploymentsv1.EdgeStackPlan {
	return &deploymentsv1.EdgeStackPlan{
		EdgeKind: string(edgeFront.Kind()),
		Items:    releasePlanItems(edgeFront, recorded),
	}
}

func releasePlanItems(edgeFront edge.Edge, recorded bootstrap.PreviewDomain) []*deploymentsv1.TeardownItem {
	removed, kept := edgeFront.PreviewWildcardSurfaces(edge.PreviewWildcard(recorded.BaseDomain))

	items := []*deploymentsv1.TeardownItem{surfaceItem(removed)}
	if arn := recorded.Settlement.Certificate.ARN; arn != "" {
		items = append(items, certificateItem(recorded.Settlement.Certificate))
	}
	for _, rec := range recorded.Settlement.WrittenRecords() {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range recorded.Settlement.OwedRecords() {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	return append(items, surfaceItem(kept))
}
