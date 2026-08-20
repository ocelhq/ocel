package server

import (
	"fmt"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type projectPlanScope struct {
	class      string
	slug       string
	stateTable string
	stacks     int
	state      edge.StackState
}

func destroyPlanItems(edgeFront edge.Edge, scope projectPlanScope) ([]*deploymentsv1.TeardownItem, error) {
	recorded, err := bootstrap.ReadProduction(scope.state)
	if err != nil {
		return nil, err
	}
	written, err := edge.WrittenRecords(scope.state)
	if err != nil {
		return nil, err
	}

	items := surfaceItems(edgeFront, scope)
	items = append(items, certificateItems(recorded)...)
	items = append(items, recordItems(written, recorded)...)
	items = append(items, storeItems(edgeFront, scope)...)
	return append(items, substrateItems(edgeFront, scope)...), nil
}

func surfaceItems(edgeFront edge.Edge, scope projectPlanScope) []*deploymentsv1.TeardownItem {
	surfaces := edgeFront.ProjectSurfaces(edge.ProjectScope{
		Slug:      scope.slug,
		Class:     edge.Class(scope.class),
		Hostnames: edge.BoundDomains(scope.state),
		Front:     edge.FrontOf(scope.state),
	})
	items := make([]*deploymentsv1.TeardownItem, 0, len(surfaces))
	for _, surface := range surfaces {
		items = append(items, surfaceItem(surface))
	}
	return items
}

func surfaceItem(surface edge.Surface) *deploymentsv1.TeardownItem {
	return &deploymentsv1.TeardownItem{
		Kind:   surface.Kind,
		Name:   surface.Name,
		Action: surfaceAction(surface.Action),
		Reason: surface.Reason,
		Slow:   surface.Slow,
	}
}

func surfaceAction(action edge.SurfaceAction) deploymentsv1.TeardownItem_Action {
	switch action {
	case edge.SurfaceKeep:
		return deploymentsv1.TeardownItem_ACTION_KEEP
	case edge.SurfaceDisableThenDelete:
		return deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE
	default:
		return deploymentsv1.TeardownItem_ACTION_DELETE
	}
}

func certificateItems(recorded domains.Settlement) []*deploymentsv1.TeardownItem {
	var items []*deploymentsv1.TeardownItem
	if arn := recorded.Certificate.ARN; arn != "" {
		items = append(items, certificateItem(recorded.Certificate))
	}
	for _, host := range recorded.Hosts {
		if host.Certificate == "" || host.Certificate == recorded.Certificate.ARN {
			continue
		}
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "certificate",
			Name:   host.Certificate,
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: fmt.Sprintf("pinned for %s in `certificates`: ocel never requested it, so it is not ocel's to delete", host.Hostname),
		})
	}
	return items
}

func certificateItem(cert certs.Certificate) *deploymentsv1.TeardownItem {
	if cert.Adopted {
		return &deploymentsv1.TeardownItem{
			Kind:   "certificate",
			Name:   cert.ARN,
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "ocel adopted it rather than requesting it, so it is not ocel's to delete",
		}
	}
	return &deploymentsv1.TeardownItem{
		Kind:   "certificate",
		Name:   cert.ARN,
		Action: deploymentsv1.TeardownItem_ACTION_DELETE,
		Reason: "ocel requested it for this project's hostnames and nothing else is served by it",
	}
}

func recordItems(written []edge.Record, recorded domains.Settlement) []*deploymentsv1.TeardownItem {
	var items []*deploymentsv1.TeardownItem
	for _, rec := range mergeRecords(written, recorded.Validation.Written, hostRecords(recorded, false)) {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range mergeRecords(recorded.Validation.Owed, hostRecords(recorded, true)) {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	return items
}

func hostRecords(recorded domains.Settlement, owed bool) []edge.Record {
	var records []edge.Record
	for _, host := range recorded.Hosts {
		if owed {
			records = append(records, host.Records.Owed...)
			continue
		}
		records = append(records, host.Records.Written...)
	}
	return records
}

func mergeRecords(lists ...[]edge.Record) []edge.Record {
	var all []edge.Record
	for _, list := range lists {
		for _, rec := range list {
			if !containsRecord(all, rec) {
				all = append(all, rec)
			}
		}
	}
	return all
}

func containsRecord(records []edge.Record, wanted edge.Record) bool {
	for _, rec := range records {
		if rec == wanted {
			return true
		}
	}
	return false
}

func storeItems(edgeFront edge.Edge, scope projectPlanScope) []*deploymentsv1.TeardownItem {
	var items []*deploymentsv1.TeardownItem
	if !edgeFront.Facts().RunsCode {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "deployments ledger",
			Name:   scope.class + "/" + scope.slug,
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "every promotion, pointer and deployment record this project holds",
		})
	}
	if scope.stacks == 0 {
		return items
	}
	return append(items, &deploymentsv1.TeardownItem{
		Kind:   "tag clock rows",
		Name:   scope.slug,
		Action: deploymentsv1.TeardownItem_ACTION_DELETE,
		Reason: "the revalidation timestamps every stack of this project wrote while it served",
	})
}

func substrateItems(edgeFront edge.Edge, scope projectPlanScope) []*deploymentsv1.TeardownItem {
	keep := &deploymentsv1.TeardownItem{
		Kind:   "state table",
		Name:   scope.stateTable,
		Action: deploymentsv1.TeardownItem_ACTION_KEEP,
		Reason: fmt.Sprintf("substrate-scoped: every %s project shares it — `%s` removes it", scope.class, teardownCommand(scope.class)),
	}
	if scope.stateTable == "" {
		keep.Name = scope.class + " substrate"
	}
	items := []*deploymentsv1.TeardownItem{keep}

	if base := scope.state[edge.StackKeyGlobalPreview]; base != "" {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "preview wildcard",
			Name:   edge.PreviewWildcard(base),
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "substrate-scoped: every project's previews are served on it — `ocel domain release --preview` releases it",
		})
	}
	return append(items, surfaceItem(edgeFront.SharedPreviewSurface()))
}
