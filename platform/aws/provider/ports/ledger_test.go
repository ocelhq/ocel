package ports_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type invalidationTargets struct {
	Table string `json:"table"`
	Class string `json:"class"`
	Items []struct {
		Slug          string   `json:"slug"`
		PK            string   `json:"pk"`
		SK            string   `json:"sk"`
		Distributions []string `json:"distributions"`
	} `json:"items"`
}

func TestTheLedgerKeepsInvalidationTargetsWhereTheTagInvalidatorReadsThem(t *testing.T) {
	raw, err := os.ReadFile("testdata/invalidation-targets.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture invalidationTargets
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}

	ddb := newFakeDynamo()
	for _, item := range fixture.Items {
		ledger := awsports.Ledger(ddb, awsports.Table(fixture.Table), edge.Class(fixture.Class), item.Slug)
		for _, distribution := range item.Distributions {
			if err := ledger.NoteInvalidationTarget(context.Background(), distribution); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, item := range fixture.Items {
		stored, ok := ddb.items[item.PK][item.SK]
		if !ok {
			t.Errorf("no item at pk %q sk %q, where the tag invalidator reads the targets for slug %q", item.PK, item.SK, item.Slug)
			continue
		}
		body, _ := stored["body"].(*ddbtypes.AttributeValueMemberB)
		if body == nil {
			t.Errorf("the item at pk %q sk %q has no binary body", item.PK, item.SK)
			continue
		}
		var got []string
		if err := json.Unmarshal(body.Value, &got); err != nil {
			t.Fatalf("the body at pk %q is not a JSON list: %v", item.PK, err)
		}
		if !slices.Equal(got, item.Distributions) {
			t.Errorf("targets at pk %q = %v, want %v", item.PK, got, item.Distributions)
		}
	}
}
