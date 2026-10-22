package cloudflare

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	resourceKindWorker    = "cloudflare:worker"
	resourceKindDNSRecord = "cloudflare:dns-record"
	resourceLocation      = "global"
)

func ListProjectResources(ctx context.Context, namespace string, origin Origin, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
	var resources []provider.PhysicalResource
	if req.EdgeKind == Kind {
		script, ok, err := projectEntryWorker(ctx, namespace, origin, req)
		if err != nil {
			return nil, err
		}
		if ok {
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
	return resources, nil
}

func projectEntryWorker(ctx context.Context, namespace string, origin Origin, req provider.ProjectResourcesRequest) (string, bool, error) {
	var own private
	if err := req.EdgeStack.Private.Into(&own); err != nil || own.EntryWorker == "" {
		return "", false, nil
	}
	scope, err := resolveOrigin(ctx, origin.Scope)
	if err != nil {
		return "", false, err
	}
	conventional, err := originNames{namespace: namespace, origin: scope}.entryWorker(req.Project, req.Tier)
	if err != nil {
		return "", false, err
	}
	return own.EntryWorker, own.EntryWorker == conventional, nil
}
