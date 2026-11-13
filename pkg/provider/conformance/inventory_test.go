package conformance

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

func TestAnInventoriedTypeNoRuleNamesIsReportedUnpriced(t *testing.T) {
	set := &costv1.ResourceSet{Resources: []*costv1.Resource{
		{Id: "a", Type: "aws_lambda_function"},
		{Id: "b", Type: "aws_invented_thing"},
		{Id: "c", Type: "aws_invented_thing"},
	}}
	table := pricing.Table{"aws_lambda_function": func(r *pricing.Subject) { r.Free() }}

	if got := unpricedTypes(set, table); !slices.Equal(got, []string{"aws_invented_thing"}) {
		t.Errorf("unpricedTypes() = %v, want the one type no rule names, once", got)
	}
}

func TestEveryTypeTheRuleSetNamesIsPriceable(t *testing.T) {
	set := &costv1.ResourceSet{Resources: []*costv1.Resource{
		{Id: "a", Type: "aws_rds_cluster"},
		{Id: "b", Type: "google_sql_database_instance"},
		{Id: "c", Type: "cloudflare_workers_script"},
	}}

	if got := unpricedTypes(set, nil); len(got) != 0 {
		t.Errorf("unpricedTypes() = %v, want none: the rule set names every one", got)
	}
}
