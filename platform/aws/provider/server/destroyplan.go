package server

import (
	"fmt"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type projectPlanScope struct {
	kind       edge.Kind
	class      string
	slug       string
	stateTable string
	stacks     int
	state      edge.StackState
}

func destroyPlanItems(scope projectPlanScope) ([]*deploymentsv1.TeardownItem, error) {
	recorded, err := bootstrap.ReadProduction(scope.state)
	if err != nil {
		return nil, err
	}
	written, err := edge.WrittenRecords(scope.state)
	if err != nil {
		return nil, err
	}

	items := surfaceItems(scope)
	items = append(items, certificateItems(recorded)...)
	items = append(items, recordItems(written, recorded)...)
	items = append(items, storeItems(scope)...)
	return append(items, substrateItems(scope)...), nil
}

func surfaceItems(scope projectPlanScope) []*deploymentsv1.TeardownItem {
	hostnames := edge.BoundDomains(scope.state)
	switch scope.kind {
	case edge.KindCloudflare:
		items := []*deploymentsv1.TeardownItem{{
			Kind:   "edge workers",
			Name:   scope.slug,
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "every per-app worker this project deployed, and the routes that reach them",
		}}
		if len(hostnames) > 0 {
			items = append(items, &deploymentsv1.TeardownItem{
				Kind:   "worker routes",
				Name:   strings.Join(hostnames, ", "),
				Action: deploymentsv1.TeardownItem_ACTION_DELETE,
				Reason: "the hostnames this project is served on stop resolving to a worker",
			})
		}
		return append(items, &deploymentsv1.TeardownItem{
			Kind:   "deployments store",
			Name:   scope.slug,
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "the store instance holding every deployment and pointer this project promoted",
		})
	case edge.KindNative:
		var items []*deploymentsv1.TeardownItem
		if front := edge.FrontOf(scope.state); front != "" {
			items = append(items, &deploymentsv1.TeardownItem{
				Kind:   "distribution",
				Name:   front,
				Action: deploymentsv1.TeardownItem_ACTION_DISABLE_THEN_DELETE,
				Reason: "the CloudFront distribution fronting this project; CloudFront only deletes a disabled distribution once the disable has reached every edge",
				Slow:   true,
			})
		}
		if len(hostnames) > 0 {
			items = append(items, &deploymentsv1.TeardownItem{
				Kind:   "edge routes",
				Name:   strings.Join(hostnames, ", "),
				Action: deploymentsv1.TeardownItem_ACTION_DELETE,
				Reason: "the key-value store entry the resolver reads for each of this project's hostnames",
			})
		}
		return items
	default:
		items := []*deploymentsv1.TeardownItem{{
			Kind:   "REST APIs",
			Name:   scope.slug,
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: restAPIsReason(scope.class),
		}}
		if len(hostnames) > 0 {
			items = append(items, &deploymentsv1.TeardownItem{
				Kind:   "domain names",
				Name:   strings.Join(hostnames, ", "),
				Action: deploymentsv1.TeardownItem_ACTION_DELETE,
				Reason: "the API Gateway domain name each hostname is mapped onto",
			})
		}
		return items
	}
}

func restAPIsReason(class string) string {
	if class == bootstrap.ClassPreview {
		return "every preview API this project is served through, and the host rules routing to them"
	}
	return "the production API and every preview API this project is served through, and the host rules routing to them"
}

func certificateItems(recorded bootstrap.Production) []*deploymentsv1.TeardownItem {
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

func recordItems(written []edge.Record, recorded bootstrap.Production) []*deploymentsv1.TeardownItem {
	var items []*deploymentsv1.TeardownItem
	for _, rec := range mergeRecords(written, recorded.Written, hostRecords(recorded, false)) {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_DELETE,
			Reason: "ocel wrote it; it is removed only while its live value is still the one ocel wrote",
		})
	}
	for _, rec := range mergeRecords(recorded.Owed, hostRecords(recorded, true)) {
		items = append(items, &deploymentsv1.TeardownItem{
			Kind:   "DNS record",
			Name:   rec.String(),
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "you created it yourself; ocel never wrote it, so it is yours to remove",
		})
	}
	return items
}

func hostRecords(recorded bootstrap.Production, owed bool) []edge.Record {
	var records []edge.Record
	for _, host := range recorded.Hosts {
		if owed {
			records = append(records, host.Owed...)
			continue
		}
		records = append(records, host.Written...)
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

func storeItems(scope projectPlanScope) []*deploymentsv1.TeardownItem {
	var items []*deploymentsv1.TeardownItem
	if scope.kind != edge.KindCloudflare {
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

func substrateItems(scope projectPlanScope) []*deploymentsv1.TeardownItem {
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
	return append(items, sharedEdgeItem(scope.kind))
}

func sharedEdgeItem(kind edge.Kind) *deploymentsv1.TeardownItem {
	switch kind {
	case edge.KindCloudflare:
		return &deploymentsv1.TeardownItem{
			Kind:   "shared preview entry worker",
			Name:   edge.PreviewEntryOwner,
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "substrate-scoped: it fronts every project's previews",
		}
	case edge.KindNative:
		return &deploymentsv1.TeardownItem{
			Kind:   "preview resolver",
			Name:   "function and key-value store",
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "substrate-scoped: every project's routes are read from it",
		}
	default:
		return &deploymentsv1.TeardownItem{
			Kind:   "preview fallback API",
			Name:   "404 responder",
			Action: deploymentsv1.TeardownItem_ACTION_KEEP,
			Reason: "substrate-scoped: it answers every preview hostname no project claims",
		}
	}
}
