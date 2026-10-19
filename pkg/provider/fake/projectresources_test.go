package fake_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestTheFakeListsEveryDNSRecordItWroteForAProjectAsItsResource(t *testing.T) {
	t.Parallel()

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	list := vendor.Hooks().ListProjectResources
	if list == nil {
		t.Fatal("the fake lists nothing a project holds outside its stacks")
	}
	written := []edge.Record{
		{Name: "shop.example", Type: edge.RecordTypeCNAME, Value: "shop.fake.invalid"},
		{Name: "www.shop.example", Type: edge.RecordTypeCNAME, Value: "shop.fake.invalid"},
	}

	listed, err := list(context.Background(), provider.ProjectResourcesRequest{
		Tier:           environment.TierProduction,
		Project:        "shop",
		WrittenRecords: written,
	})
	if err != nil {
		t.Fatalf("ListProjectResources() = %v", err)
	}
	want := []provider.PhysicalResource{
		{Kind: "fake:dns-record", ID: written[0].String()},
		{Kind: "fake:dns-record", ID: written[1].String()},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("ListProjectResources() = %v, want %v", listed, want)
	}
}
