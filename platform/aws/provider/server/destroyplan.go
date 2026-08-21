package server

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
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
	record     bootstrap.StackRecord
}

func destroyPlanItems(edgeFront edge.Edge, scope projectPlanScope) []*contractv1.RemovalItem {
	recorded := scope.record.Production

	items := surfaceItems(edgeFront, scope)
	items = append(items, certificateItems(recorded)...)
	items = append(items, recordItems(scope.record.Edge.Records, recorded)...)
	items = append(items, storeItems(edgeFront, scope)...)
	return append(items, bootstrapItems(edgeFront, scope)...)
}

func stackItems(infra, app []naming.StackName) []*contractv1.RemovalItem {
	items := make([]*contractv1.RemovalItem, 0, len(infra)+len(app))
	for _, stack := range infra {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "infra stack",
			Name:   stack.String(),
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "databases and buckets, INCLUDING ALL DATA",
		})
	}
	for _, stack := range app {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "app stack",
			Name:   stack.String(),
			Action: contractv1.RemovalItem_ACTION_DELETE,
		})
	}
	return items
}

func projectIndexItem(slug string) *contractv1.RemovalItem {
	return &contractv1.RemovalItem{
		Kind:   "project index entry",
		Name:   slug,
		Action: contractv1.RemovalItem_ACTION_DELETE,
		Reason: "the record that this account knows this project at all",
	}
}

func surfaceItems(edgeFront edge.Edge, scope projectPlanScope) []*contractv1.RemovalItem {
	surfaces := edgeFront.ProjectSurfaces(edge.ProjectScope{
		Slug:      scope.slug,
		Class:     edge.Class(scope.class),
		Hostnames: scope.record.Edge.Bound,
		Front:     scope.record.Edge.Front,
	})
	items := make([]*contractv1.RemovalItem, 0, len(surfaces))
	for _, surface := range surfaces {
		items = append(items, surfaceItem(surface))
	}
	return items
}

func surfaceItem(surface edge.Surface) *contractv1.RemovalItem {
	return &contractv1.RemovalItem{
		Kind:   surface.Kind,
		Name:   surface.Name,
		Action: surfaceAction(surface.Action),
		Reason: surface.Reason,
		Slow:   surface.Slow,
	}
}

func surfaceAction(action edge.SurfaceAction) contractv1.RemovalItem_Action {
	switch action {
	case edge.SurfaceKeep:
		return contractv1.RemovalItem_ACTION_KEEP
	case edge.SurfaceDisableThenDelete:
		return contractv1.RemovalItem_ACTION_DISABLE_THEN_DELETE
	default:
		return contractv1.RemovalItem_ACTION_DELETE
	}
}

func certificateItems(recorded domains.Settlement) []*contractv1.RemovalItem {
	var items []*contractv1.RemovalItem
	if arn := recorded.Certificate.ARN; arn != "" {
		items = append(items, certificateItem(recorded.Certificate))
	}
	for _, host := range recorded.Hosts {
		if host.Certificate == "" || host.Certificate == recorded.Certificate.ARN {
			continue
		}
		items = append(items, &contractv1.RemovalItem{
			Kind:   "certificate",
			Name:   host.Certificate,
			Action: contractv1.RemovalItem_ACTION_KEEP,
			Reason: fmt.Sprintf("pinned for %s in `certificates`: ocel never requested it, so it is not ocel's to delete", host.Hostname),
		})
	}
	return items
}

func certificateItem(cert certs.Certificate) *contractv1.RemovalItem {
	if cert.Adopted {
		return &contractv1.RemovalItem{
			Kind:   "certificate",
			Name:   cert.ARN,
			Action: contractv1.RemovalItem_ACTION_KEEP,
			Reason: "ocel adopted it rather than requesting it, so it is not ocel's to delete",
		}
	}
	return &contractv1.RemovalItem{
		Kind:   "certificate",
		Name:   cert.ARN,
		Action: contractv1.RemovalItem_ACTION_DELETE,
		Reason: "ocel requested it for this project's hostnames and nothing else is served by it",
	}
}

func recordItems(written []edge.Record, recorded domains.Settlement) []*contractv1.RemovalItem {
	var items []*contractv1.RemovalItem
	for _, rec := range mergeRecords(written, recorded.Validation.Written, hostRecords(recorded, false)) {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range mergeRecords(recorded.Validation.Owed, hostRecords(recorded, true)) {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: contractv1.RemovalItem_ACTION_KEEP,
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

func storeItems(edgeFront edge.Edge, scope projectPlanScope) []*contractv1.RemovalItem {
	var items []*contractv1.RemovalItem
	if !edgeFront.Facts().RunsCode {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "deployments ledger",
			Name:   scope.class + "/" + scope.slug,
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "every promotion, pointer and deployment record this project holds",
		})
	}
	if scope.stacks == 0 {
		return items
	}
	return append(items, &contractv1.RemovalItem{
		Kind:   "tag clock rows",
		Name:   scope.slug,
		Action: contractv1.RemovalItem_ACTION_DELETE,
		Reason: "the revalidation timestamps every stack of this project wrote while it served",
	})
}

func bootstrapItems(edgeFront edge.Edge, scope projectPlanScope) []*contractv1.RemovalItem {
	keep := &contractv1.RemovalItem{
		Kind:   "state table",
		Name:   scope.stateTable,
		Action: contractv1.RemovalItem_ACTION_KEEP,
		Reason: fmt.Sprintf("bootstrap-scoped: every %s project shares it — `%s` removes it", scope.class, teardownCommand(scope.class)),
	}
	if scope.stateTable == "" {
		keep.Name = scope.class + " bootstrap"
	}
	items := []*contractv1.RemovalItem{keep}

	if base := scope.record.Edge.GlobalPreview; base != "" {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "preview wildcard",
			Name:   edge.PreviewWildcard(base),
			Action: contractv1.RemovalItem_ACTION_KEEP,
			Reason: "bootstrap-scoped: every project's previews are served on it — `ocel domain release --preview` releases it",
		})
	}
	return append(items, surfaceItem(edgeFront.SharedPreviewSurface()))
}
