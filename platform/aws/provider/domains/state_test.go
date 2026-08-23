package domains

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func newState() (State, *fake.Records) {
	records := fake.NewRecords()
	return State{Records: records}, records
}

func settled() Settlement {
	return Settlement{
		Certificate: certs.Certificate{ARN: "arn:ocel", Region: "us-east-1", Status: certs.StatusIssued},
		Validation:  Records{Written: []edge.Record{{Name: "_ocel.app.com", Type: edge.RecordTypeCNAME, Value: "_v.acm-validations.aws"}}},
		Hosts: []Host{{
			Hostname:    "shop.app.com",
			Certificate: "arn:ocel",
			Records:     Records{Written: []edge.Record{{Name: "shop.app.com", Type: edge.RecordTypeCNAME, Value: "d1.cloudfront.net"}}},
			Probe:       certs.Probe{At: time.Unix(1755500000, 0).UTC(), Edge: "cloudfront", OK: true},
		}},
	}
}

func TestStackRecordCarriesASettlementThroughTheKitsState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state, _ := newState()
	held := StackRecord{}.With(settled())
	held.Edge = edge.StackState{Slug: "shop", Class: edge.ClassProduction}

	if err := state.WriteStack(ctx, edge.ClassProduction, "shop", held); err != nil {
		t.Fatalf("WriteStack: %v", err)
	}
	read, err := state.ReadStack(ctx, edge.ClassProduction, "shop")
	if err != nil {
		t.Fatalf("ReadStack: %v", err)
	}
	if !reflect.DeepEqual(read.Settlement(), settled()) {
		t.Errorf("settlement = %+v, want the one recorded: %+v", read.Settlement(), settled())
	}
	if read.Edge.Slug != "shop" {
		t.Errorf("edge state = %+v, want the stack the edge left standing", read.Edge)
	}
}

func TestAStackRecordReadsAsTheKitsEdgeStackState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state, records := newState()
	held := StackRecord{}.With(settled())
	held.Edge = edge.StackState{Slug: "shop", Bound: []string{"shop.app.com"}}
	if err := state.WriteStack(ctx, edge.ClassProduction, "shop", held); err != nil {
		t.Fatalf("WriteStack: %v", err)
	}

	row, err := records.Read(ctx, providerkit.EdgeStackRecord(edge.ClassProduction, "shop"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var kitState providerkit.EdgeStackState
	if err := json.Unmarshal(row.Bytes, &kitState); err != nil {
		t.Fatalf("read the record as the kit reads it: %v", err)
	}
	if !reflect.DeepEqual(kitState.Hostnames(), []string{"shop.app.com"}) {
		t.Errorf("hostnames = %v, want the kit to read the hosts this provider settled", kitState.Hostnames())
	}
	if !kitState.Ready("shop.app.com", "cloudfront") {
		t.Error("Ready = false, want the kit to read the probe this provider recorded")
	}
	if kitState.Host("shop.app.com").Certificate != "arn:ocel" {
		t.Errorf("certificate = %q, want the certificate bound to the host", kitState.Host("shop.app.com").Certificate)
	}
}

func TestStackSlugsNamesEveryProjectWithAnEdgeStackInItsClass(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state, _ := newState()
	for _, slug := range []string{"shop", "blog"} {
		held := StackRecord{}
		held.Edge = edge.StackState{Slug: slug}
		if err := state.WriteStack(ctx, edge.ClassPreview, slug, held); err != nil {
			t.Fatalf("WriteStack(%s): %v", slug, err)
		}
	}
	held := StackRecord{}
	held.Edge = edge.StackState{Slug: "docs"}
	if err := state.WriteStack(ctx, edge.ClassProduction, "docs", held); err != nil {
		t.Fatalf("WriteStack(docs): %v", err)
	}

	slugs, err := state.StackSlugs(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("StackSlugs: %v", err)
	}
	if !reflect.DeepEqual(slugs, []string{"blog", "shop"}) {
		t.Errorf("slugs = %v, want the preview class alone: a production stack is not a preview one", slugs)
	}
}

func TestForgetStackLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state, _ := newState()
	held := StackRecord{}
	held.Edge = edge.StackState{Slug: "shop"}
	if err := state.WriteStack(ctx, edge.ClassProduction, "shop", held); err != nil {
		t.Fatalf("WriteStack: %v", err)
	}
	if err := state.ForgetStack(ctx, edge.ClassProduction, "shop"); err != nil {
		t.Fatalf("ForgetStack: %v", err)
	}
	read, err := state.ReadStack(ctx, edge.ClassProduction, "shop")
	if err != nil {
		t.Fatalf("ReadStack: %v", err)
	}
	if !read.Empty() {
		t.Errorf("record = %+v, want a torn-down project to leave none", read)
	}
	if err := state.ForgetStack(ctx, edge.ClassProduction, "shop"); err != nil {
		t.Errorf("repeated ForgetStack = %v, want a re-run of a finished teardown to pass", err)
	}
}

func TestWildcardRecordRoundTripsItsSettlement(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state, records := newState()
	wildcard := PreviewWildcard{Wildcard: providerkit.Wildcard{
		BaseDomain: "preview.acme.com",
		Edge:       "cloudfront",
		Scope:      "acct",
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
	}}
	held := wildcard.With(Settlement{
		Certificate: certs.Certificate{ARN: "arn:ocel"},
		Hosts: []Host{{
			Hostname: "*.preview.acme.com",
			Records:  Records{Written: []edge.Record{{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}}},
			Probe:    certs.Probe{At: time.Unix(1755500000, 0).UTC(), Edge: "cloudfront", OK: true},
		}},
	})

	if err := state.WriteWildcard(ctx, edge.ClassPreview, held); err != nil {
		t.Fatalf("WriteWildcard: %v", err)
	}
	read, err := state.ReadWildcard(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("ReadWildcard: %v", err)
	}
	if read.BaseDomain != "preview.acme.com" || read.Edge != "cloudfront" || read.Scope != "acct" {
		t.Errorf("wildcard = %+v, want the domain, its edge and its scope", read)
	}
	if !read.Settled.Probe.OK || read.Settled.Probe.Edge != "cloudfront" {
		t.Errorf("probe = %+v, want the wildcard proven against the edge holding it", read.Settled.Probe)
	}
	if read.Certificate.ARN != "arn:ocel" {
		t.Errorf("certificate = %+v, want the one settled for the wildcard", read.Certificate)
	}

	row, err := records.Read(ctx, providerkit.WildcardRecord(edge.ClassPreview))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var kitWildcard providerkit.Wildcard
	if err := json.Unmarshal(row.Bytes, &kitWildcard); err != nil {
		t.Fatalf("read the record as the kit reads it: %v", err)
	}
	if kitWildcard.Hostname() != "*.preview.acme.com" || kitWildcard.Settled.Serving() != "cloudfront" {
		t.Errorf("kit wildcard = %+v, want the kit to read what this provider settled", kitWildcard)
	}

	if err := state.ForgetWildcard(ctx, edge.ClassPreview); err != nil {
		t.Fatalf("ForgetWildcard: %v", err)
	}
	gone, err := state.ReadWildcard(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("ReadWildcard after release: %v", err)
	}
	if gone.BaseDomain != "" {
		t.Errorf("wildcard = %+v, want a released domain to leave no record", gone)
	}
}

func TestReadingWhatWasNeverWrittenIsEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	state, _ := newState()

	record, err := state.ReadStack(ctx, edge.ClassProduction, "never-deployed")
	if err != nil {
		t.Fatalf("ReadStack: %v", err)
	}
	if !record.Empty() {
		t.Errorf("record = %+v, want nothing recorded for a project that never deployed", record)
	}
	wildcard, err := state.ReadWildcard(ctx, edge.ClassPreview)
	if err != nil {
		t.Fatalf("ReadWildcard: %v", err)
	}
	if wildcard.BaseDomain != "" {
		t.Errorf("wildcard = %+v, want nothing recorded before a domain is used", wildcard)
	}
}
