package server

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type countingRecords struct {
	*fake.Records
	writes int
}

func newRecords() *countingRecords {
	return &countingRecords{Records: fake.NewRecords()}
}

func (r *countingRecords) Write(ctx context.Context, record kit.Record) (kit.Revision, error) {
	r.writes++
	return r.Records.Write(ctx, record)
}

func newRecordState() domains.State {
	return domains.State{Records: newRecords()}
}

func recordStateOf(records kit.RecordStore) domains.State {
	return domains.State{Records: records}
}

func previewWildcardWithScope(baseDomain string, kind edge.Kind, scope string) domains.PreviewWildcard {
	held := previewWildcardOn(baseDomain, kind)
	held.Scope = scope
	return held
}

func previewWildcardOn(baseDomain string, kind edge.Kind) domains.PreviewWildcard {
	return domains.PreviewWildcard{Wildcard: providerkit.Wildcard{BaseDomain: baseDomain, Edge: kind}}
}
