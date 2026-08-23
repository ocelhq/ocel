package server

import (
	"fmt"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
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

func releasePlan(edgeFront edge.Edge, recorded domains.PreviewWildcard) *contractv1.RemovalPlan {
	return &contractv1.RemovalPlan{
		EdgeKind: string(edgeFront.Kind()),
		Items:    releasePlanItems(edgeFront, recorded),
		Subject:  recorded.BaseDomain,
	}
}

func releasePlanItems(edgeFront edge.Edge, recorded domains.PreviewWildcard) []*contractv1.RemovalItem {
	removed, kept := edgeFront.PreviewWildcardSurfaces(edge.PreviewWildcard(recorded.BaseDomain))
	settled := recorded.Settlement()

	items := []*contractv1.RemovalItem{surfaceItem(removed)}
	if arn := recorded.Certificate.ARN; arn != "" {
		items = append(items, certificateItem(recorded.Certificate))
	}
	for _, rec := range settled.WrittenRecords() {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range settled.OwedRecords() {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.RemovalItem_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	return append(items, surfaceItem(kept))
}
