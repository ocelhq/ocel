package stackindex

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
	written  []map[string]ddbtypes.AttributeValue
	deleted  []map[string]ddbtypes.AttributeValue
}

func (d *recordingDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	d.written = append(d.written, in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (d *recordingDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.deleted = append(d.deleted, in.Key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (d *recordingDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	d.queries = append(d.queries, in)
	index := aws.ToString(in.IndexName)
	if index != "" && index != IndexName {
		return nil, fmt.Errorf("recordingDynamo: no index named %q", index)
	}
	if index != "" && aws.ToBool(in.ConsistentRead) {
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
	if index != "" {
		matched = asProjected(matched)
	}
	return d.paginate(matched, in.ExclusiveStartKey), nil
}

func conditionHolds(row map[string]ddbtypes.AttributeValue, in *dynamodb.QueryInput) (bool, error) {
	for _, clause := range strings.Split(aws.ToString(in.KeyConditionExpression), " AND ") {
		attribute, operand, prefix, err := clauseOf(clause, in)
		if err != nil {
			return false, err
		}
		stored, ok := row[attribute].(*ddbtypes.AttributeValueMemberS)
		switch {
		case !ok:
			return false, nil
		case prefix && !strings.HasPrefix(stored.Value, operand):
			return false, nil
		case !prefix && stored.Value != operand:
			return false, nil
		}
	}
	return true, nil
}

func clauseOf(clause string, in *dynamodb.QueryInput) (string, string, bool, error) {
	clause = strings.TrimSpace(clause)
	var fields []string
	prefix := strings.HasPrefix(clause, "begins_with(")
	if prefix {
		fields = strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(clause, "begins_with("), ")"), ", ", 2)
	} else {
		fields = strings.SplitN(clause, " = ", 2)
	}
	if len(fields) != 2 {
		return "", "", false, fmt.Errorf("recordingDynamo: cannot read key condition %q", clause)
	}
	attribute, ok := in.ExpressionAttributeNames[fields[0]]
	if !ok {
		return "", "", false, fmt.Errorf("recordingDynamo: %q names no attribute", fields[0])
	}
	operand, ok := in.ExpressionAttributeValues[fields[1]].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return "", "", false, fmt.Errorf("recordingDynamo: %q carries no string value", fields[1])
	}
	return attribute, operand.Value, prefix, nil
}

func asProjected(rows []map[string]ddbtypes.AttributeValue) []map[string]ddbtypes.AttributeValue {
	out := make([]map[string]ddbtypes.AttributeValue, 0, len(rows))
	for _, row := range rows {
		projected := map[string]ddbtypes.AttributeValue{}
		for _, name := range []string{"pk", sortAttribute, tagNamespaceAttribute, "gsi1sk", "expired", "stale", "tag"} {
			if value, ok := row[name]; ok {
				projected[name] = value
			}
		}
		out = append(out, projected)
	}
	return out
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

func stackRow(project string, stack naming.StackName) map[string]ddbtypes.AttributeValue {
	return item(naming.ProjectKey(project), naming.StackKey(project, stack))
}

func projectRow(project string) map[string]ddbtypes.AttributeValue {
	return item(projectsPartition, naming.ProjectKey(project))
}

func tagRow(project string, stack naming.StackName, tag string) map[string]ddbtypes.AttributeValue {
	entry := item(naming.ISRTagKey(project, stack, tag), tagSortKey)
	entry[tagNamespaceAttribute] = &ddbtypes.AttributeValueMemberS{Value: naming.ISRTagPrefix(project, stack)}
	entry["gsi1sk"] = &ddbtypes.AttributeValueMemberS{Value: "000001700000000"}
	entry["tag"] = &ddbtypes.AttributeValueMemberS{Value: tag}
	return entry
}

func stackRows(project string, stacks ...naming.StackName) []map[string]ddbtypes.AttributeValue {
	rows := make([]map[string]ddbtypes.AttributeValue, 0, len(stacks))
	for _, stack := range stacks {
		rows = append(rows, stackRow(project, stack))
	}
	return rows
}

func projectRows(projects ...string) []map[string]ddbtypes.AttributeValue {
	rows := make([]map[string]ddbtypes.AttributeValue, 0, len(projects))
	for _, project := range projects {
		rows = append(rows, projectRow(project))
	}
	return rows
}

func deletedKeys(entries []map[string]ddbtypes.AttributeValue) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, valueOf(entry, "pk")+" "+valueOf(entry, sortAttribute))
	}
	return keys
}

func release(t *testing.T, token string) naming.Release {
	t.Helper()
	r, err := naming.ParseRelease(token)
	if err != nil {
		t.Fatalf("ParseRelease(%q): %v", token, err)
	}
	return r
}

func partitionOf(t *testing.T, in *dynamodb.QueryInput) string {
	t.Helper()
	pk, ok := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		t.Fatalf("query carries no partition: %v", in.ExpressionAttributeValues)
	}
	return pk.Value
}

func stringAttr(t *testing.T, entry map[string]ddbtypes.AttributeValue, name string) string {
	t.Helper()
	value, ok := entry[name].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		t.Fatalf("item %v carries no %s", entry, name)
	}
	return value.Value
}

func TestStacksPagesToTheEnd(t *testing.T) {
	t.Parallel()

	want := []naming.StackName{
		naming.InfraStack("prod"),
		naming.AppStack("prod", "web", release(t, "rcafef00d")),
		naming.AppStack("pr-7", "web", release(t, "rdeadbeef")),
	}
	ddb := &recordingDynamo{rows: stackRows("shop", want...), pageSize: 2}
	ix := &Index{Dynamo: ddb, Table: "state"}

	got, err := ix.Stacks(context.Background(), "shop")
	if err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Stacks = %v, want %v", got, want)
	}
	if len(ddb.queries) != 2 {
		t.Fatalf("issued %d queries, want the second page fetched", len(ddb.queries))
	}
	for i, q := range ddb.queries {
		if pk := partitionOf(t, q); pk != naming.ProjectKey("shop") {
			t.Fatalf("query %d partition = %q, want %q", i, pk, naming.ProjectKey("shop"))
		}
	}
}

func TestReadsAreConsistent(t *testing.T) {
	t.Parallel()

	stacks := &recordingDynamo{rows: stackRows("shop", naming.InfraStack("prod"))}
	if _, err := (&Index{Dynamo: stacks, Table: "state"}).Stacks(context.Background(), "shop"); err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	projects := &recordingDynamo{rows: projectRows("shop")}
	if _, err := (&Index{Dynamo: projects, Table: "state"}).Projects(context.Background()); err != nil {
		t.Fatalf("Projects: %v", err)
	}
	for _, ddb := range []*recordingDynamo{stacks, projects} {
		for i, q := range ddb.queries {
			if !aws.ToBool(q.ConsistentRead) {
				t.Errorf("query %d read eventually: a teardown reads back what it just wrote", i)
			}
		}
	}
}

func TestProjectsAndStacksLiveInDifferentPartitions(t *testing.T) {
	t.Parallel()

	ddb := &recordingDynamo{rows: projectRows("billing", "shop")}
	ix := &Index{Dynamo: ddb, Table: "state"}

	got, err := ix.Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if want := []string{"billing", "shop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Projects = %v, want %v", got, want)
	}
	pk := partitionOf(t, ddb.queries[0])
	if pk != projectsPartition {
		t.Fatalf("Projects queried %q, want %q", pk, projectsPartition)
	}
	if pk == naming.ProjectKey("shop") {
		t.Fatal("the registry shares a partition with a project's stacks")
	}
}

func TestEveryWrittenKeyCarriesItsProject(t *testing.T) {
	t.Parallel()

	stack := naming.AppStack("pr-7", "web", release(t, "rcafef00d"))
	ddb := &recordingDynamo{}
	ix := &Index{Dynamo: ddb, Table: "state"}
	ctx := context.Background()

	if err := ix.AddProject(ctx, "shop"); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := ix.AddStack(ctx, "shop", stack); err != nil {
		t.Fatalf("AddStack: %v", err)
	}
	if err := ix.RemoveStack(ctx, "shop", stack); err != nil {
		t.Fatalf("RemoveStack: %v", err)
	}
	if err := ix.RemoveProject(ctx, "shop"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}

	entries := append(append([]map[string]ddbtypes.AttributeValue{}, ddb.written...), ddb.deleted...)
	if len(entries) != 4 {
		t.Fatalf("wrote %d items, want one per call", len(entries))
	}
	for _, entry := range entries {
		sk := stringAttr(t, entry, sortAttribute)
		project, err := naming.ProjectOf(sk)
		if err != nil {
			t.Fatalf("ProjectOf(%q): %v", sk, err)
		}
		if project != "shop" {
			t.Fatalf("ProjectOf(%q) = %q, want shop", sk, project)
		}
		pk := stringAttr(t, entry, "pk")
		if pk == projectsPartition {
			continue
		}
		if project, err := naming.ProjectOf(pk); err != nil || project != "shop" {
			t.Fatalf("ProjectOf(%q) = %q, %v, want shop", pk, project, err)
		}
	}

	stacked := ddb.written[1]
	if got := stringAttr(t, stacked, sortAttribute); got != "PROJECT#shop#STACK#pr-7--web--rcafef00d" {
		t.Fatalf("stack key = %q, want the PROJECT#…#STACK#… grammar", got)
	}
	project, parsed, err := naming.ParseStackKey(stringAttr(t, stacked, sortAttribute))
	if err != nil {
		t.Fatalf("ParseStackKey: %v", err)
	}
	if project != "shop" {
		t.Fatalf("stack key carried project %q, want %q", project, "shop")
	}
	if parsed != stack {
		t.Fatalf("stack round-tripped as %v, want %v", parsed, stack)
	}
}

func TestUnreadableNamesAreRefused(t *testing.T) {
	t.Parallel()

	ix := &Index{Dynamo: &recordingDynamo{}, Table: "state"}
	ctx := context.Background()

	if err := ix.AddProject(ctx, ""); err == nil {
		t.Error("AddProject with no project err = nil, want it refused")
	}
	if err := ix.AddStack(ctx, "", naming.InfraStack("prod")); err == nil {
		t.Error("AddStack with no project err = nil, want it refused")
	}
	if err := ix.AddStack(ctx, "shop", naming.StackName{}); err == nil {
		t.Error("AddStack with no stack err = nil, want it refused")
	}
	if err := ix.RemoveStack(ctx, "shop", naming.StackName{Env: "prod", App: "web"}); err == nil {
		t.Error("AddStack with a releaseless app stack err = nil, want it refused")
	}
	if _, err := ix.Stacks(ctx, ""); err == nil {
		t.Error("Stacks with no project err = nil, want it refused")
	}
	if err := ix.RemoveProject(ctx, ""); err == nil {
		t.Error("RemoveProject with no project err = nil, want it refused")
	}
}

func TestAStoredNameThatCannotBeReadStopsTheListing(t *testing.T) {
	t.Parallel()

	ddb := &recordingDynamo{rows: []map[string]ddbtypes.AttributeValue{
		item(naming.ProjectKey("shop"), "PROJECT#shop#STACK#prod--web"),
	}}
	if _, err := (&Index{Dynamo: ddb, Table: "state"}).Stacks(context.Background(), "shop"); err == nil {
		t.Fatal("Stacks over an unparseable entry err = nil, want the teardown told rather than silently short")
	}
}
