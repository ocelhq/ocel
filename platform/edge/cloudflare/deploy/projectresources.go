package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	resourceKindWorker    = "cloudflare:worker"
	resourceKindDNSRecord = "cloudflare:dns-record"
	resourceLocation      = "global"
)

func ListProjectResources(namespace string, req provider.ProjectResourcesRequest) []provider.PhysicalResource {
	var resources []provider.PhysicalResource
	if req.EdgeKind == Kind {
		if script, ok := projectEntryWorker(namespace, req); ok {
			resources = append(resources, provider.PhysicalResource{Kind: resourceKindWorker, ID: script, Location: resourceLocation})
		}
	}
	var records []edge.Record
	if req.DNSKind == provider.DNSKind(Kind) {
		records = append(records, req.WrittenRecords...)
	}
	if req.EdgeKind == Kind {
		records = append(records, req.EdgeStack.Records...)
	}
	listed := map[string]bool{}
	for _, record := range records {
		id := record.String()
		if listed[id] {
			continue
		}
		listed[id] = true
		resources = append(resources, provider.PhysicalResource{Kind: resourceKindDNSRecord, ID: id, Location: resourceLocation})
	}
	return resources
}

func projectEntryWorker(namespace string, req provider.ProjectResourcesRequest) (string, bool) {
	var own private
	if err := req.EdgeStack.Private.Into(&own); err != nil || own.EntryWorker == "" {
		return "", false
	}
	conventional, err := entryWorkerName(namespace, req.Project, req.Tier)
	if err != nil || own.EntryWorker != conventional {
		return "", false
	}
	return own.EntryWorker, true
}
