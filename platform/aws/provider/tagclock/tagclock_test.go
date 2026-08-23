package tagclock

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

type recordingDynamo struct {
	rows     []map[string]ddbtypes.AttributeValue
	pageSize int
	queries  []*dynamodb.QueryInput
	deleted  []map[string]ddbtypes.AttributeValue
}

func (d *recordingDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.deleted = append(d.deleted, in.Key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (d *recordingDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	d.queries = append(d.queries, in)
	index := aws.ToString(in.IndexName)
	if index != IndexName {
		return nil, fmt.Errorf("recordingDynamo: no index named %q", index)
	}
	if aws.ToBool(in.ConsistentRead) {
		return nil, fmt.Errorf("recordingDynamo: index %q cannot be read consistently", index)
	}
	var matched []map[string]ddbtypes.AttributeValue
	for _, row := range d.rows {
		held, err := conditionHolds(row, in)
		if err != nil {
			return nil, err
		}
		if held {
			matched = append(matched, row)
		}
	}
	return d.paginate(matched, in.ExclusiveStartKey), nil
}

func conditionHolds(row map[string]ddbtypes.AttributeValue, in *dynamodb.QueryInput) (bool, error) {
	for _, clause := range strings.Split(aws.ToString(in.KeyConditionExpression), " AND ") {
		fields := strings.SplitN(strings.TrimSpace(clause), " = ", 2)
		if len(fields) != 2 {
			return false, fmt.Errorf("recordingDynamo: cannot read key condition %q", clause)
		}
		attribute, ok := in.ExpressionAttributeNames[fields[0]]
		if !ok {
			return false, fmt.Errorf("recordingDynamo: %q names no attribute", fields[0])
		}
		operand, ok := in.ExpressionAttributeValues[fields[1]].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return false, fmt.Errorf("recordingDynamo: %q carries no string value", fields[1])
		}
		stored, ok := row[attribute].(*ddbtypes.AttributeValueMemberS)
		if !ok || stored.Value != operand.Value {
			return false, nil
		}
	}
	return true, nil
}

func (d *recordingDynamo) paginate(rows []map[string]ddbtypes.AttributeValue, start map[string]ddbtypes.AttributeValue) *dynamodb.QueryOutput {
	if len(start) > 0 {
		for i, row := range rows {
			if valueOf(row, "pk") == valueOf(start, "pk") && valueOf(row, sortAttribute) == valueOf(start, sortAttribute) {
				rows = rows[i+1:]
				break
			}
		}
	}
	if d.pageSize > 0 && len(rows) > d.pageSize {
		page := rows[:d.pageSize]
		last := page[len(page)-1]
		return &dynamodb.QueryOutput{Items: page, LastEvaluatedKey: item(valueOf(last, "pk"), valueOf(last, sortAttribute))}
	}
	return &dynamodb.QueryOutput{Items: rows}
}

func valueOf(entry map[string]ddbtypes.AttributeValue, name string) string {
	value, ok := entry[name].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func tagRow(project string, stack naming.StackName, tag string) map[string]ddbtypes.AttributeValue {
	entry := item(naming.ISRTagKey(project, stack, tag), tagSortKey)
	entry[tagNamespaceAttribute] = &ddbtypes.AttributeValueMemberS{Value: naming.ISRTagPrefix(project, stack)}
	entry["gsi1sk"] = &ddbtypes.AttributeValueMemberS{Value: "000001700000000"}
	entry["tag"] = &ddbtypes.AttributeValueMemberS{Value: tag}
	return entry
}

func deletedKeys(entries []map[string]ddbtypes.AttributeValue) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, valueOf(entry, "pk")+" "+valueOf(entry, sortAttribute))
	}
	return keys
}

func release(t *testing.T, id string) naming.Release {
	t.Helper()
	return naming.NewRelease(id, "")
}

func TestSweepTakesEveryTagRowTheStackWrote(t *testing.T) {
	t.Parallel()

	stack := naming.AppStack("prod", "web", release(t, "rcafef00d"))
	survivor := naming.AppStack("prod", "web", release(t, "rdeadbeef"))
	ddb := &recordingDynamo{pageSize: 1, rows: []map[string]ddbtypes.AttributeValue{
		tagRow("shop", stack, "products"),
		tagRow("shop", stack, "carts"),
		tagRow("shop", survivor, "products"),
	}}
	sweeper := &Sweeper{Dynamo: ddb, Table: "state"}

	if err := sweeper.SweepTagClock(context.Background(), "shop", stack); err != nil {
		t.Fatalf("SweepTagClock: %v", err)
	}

	want := []string{
		naming.ISRTagKey("shop", stack, "products") + " " + tagSortKey,
		naming.ISRTagKey("shop", stack, "carts") + " " + tagSortKey,
	}
	if got := deletedKeys(ddb.deleted); !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted %v, want %v — a dropped stack leaves no tag rows behind, and none of a surviving stack's", got, want)
	}

	read := ddb.queries[0]
	if got := read.ExpressionAttributeValues[":ns"].(*ddbtypes.AttributeValueMemberS).Value; got != naming.ISRTagPrefix("shop", stack) {
		t.Fatalf("namespace = %q, want %q", got, naming.ISRTagPrefix("shop", stack))
	}
	if len(ddb.queries) != 2 {
		t.Fatalf("issued %d queries, want the tag rows paged to the end", len(ddb.queries))
	}
}
