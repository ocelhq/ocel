package stackindex

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestRemoveStackTakesTheTagClockWithIt(t *testing.T) {
	t.Parallel()

	stack := naming.AppStack("prod", "web", release(t, "rcafef00d"))
	survivor := naming.AppStack("prod", "web", release(t, "rdeadbeef"))
	ddb := &recordingDynamo{pageSize: 1, rows: []map[string]ddbtypes.AttributeValue{
		tagRow("shop", stack, "products"),
		tagRow("shop", stack, "carts"),
		tagRow("shop", survivor, "products"),
		stackRow("shop", stack),
		stackRow("shop", survivor),
	}}
	ix := &Index{Dynamo: ddb, Table: "state"}

	if err := ix.RemoveStack(context.Background(), "shop", stack); err != nil {
		t.Fatalf("RemoveStack: %v", err)
	}

	want := []string{
		naming.ISRTagKey("shop", stack, "products") + " " + tagSortKey,
		naming.ISRTagKey("shop", stack, "carts") + " " + tagSortKey,
		naming.ProjectKey("shop") + " " + naming.StackKey("shop", stack),
	}
	if got := deletedKeys(ddb.deleted); !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted %v, want %v — a dropped stack leaves no tag rows behind", got, want)
	}

	read := ddb.queries[0]
	if aws.ToString(read.IndexName) != IndexName {
		t.Fatalf("tag rows read from index %q, want %q — each row is its own partition", aws.ToString(read.IndexName), IndexName)
	}
	if got := valueOf(read.ExpressionAttributeValues, ":ns"); got != naming.ISRTagPrefix("shop", stack) {
		t.Fatalf("namespace = %q, want %q", got, naming.ISRTagPrefix("shop", stack))
	}
	if len(ddb.queries) != 2 {
		t.Fatalf("issued %d queries, want the tag rows paged to the end", len(ddb.queries))
	}
}

func TestRemoveProjectDropsOnlyTheRegistryRow(t *testing.T) {
	t.Parallel()

	ddb := &recordingDynamo{rows: projectRows("shop")}
	ix := &Index{Dynamo: ddb, Table: "state"}

	if err := ix.RemoveProject(context.Background(), "shop"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}

	want := []string{projectsPartition + " " + naming.ProjectKey("shop")}
	if got := deletedKeys(ddb.deleted); !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted %v, want %v — forgetting a project deletes only what it recognises", got, want)
	}
	if len(ddb.queries) != 0 {
		t.Fatalf("issued %d queries, want a project forgotten only once its stacks already went", len(ddb.queries))
	}
}
