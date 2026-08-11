package stackindex

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

type recordingDynamo struct {
	pages   [][]map[string]ddbtypes.AttributeValue
	queries []*dynamodb.QueryInput
	written []map[string]ddbtypes.AttributeValue
	deleted []map[string]ddbtypes.AttributeValue
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
	page := d.pages[len(d.queries)-1]
	out := &dynamodb.QueryOutput{Items: page}
	if len(d.queries) < len(d.pages) {
		out.LastEvaluatedKey = item("more", "more")
	}
	return out, nil
}

func stackPages(project string, pages ...[]naming.StackName) [][]map[string]ddbtypes.AttributeValue {
	out := make([][]map[string]ddbtypes.AttributeValue, 0, len(pages))
	for _, page := range pages {
		entries := make([]map[string]ddbtypes.AttributeValue, 0, len(page))
		for _, stack := range page {
			entries = append(entries, map[string]ddbtypes.AttributeValue{
				sortAttribute: &ddbtypes.AttributeValueMemberS{Value: naming.StackKey(project, stack)},
			})
		}
		out = append(out, entries)
	}
	return out
}

func projectPage(projects ...string) [][]map[string]ddbtypes.AttributeValue {
	entries := make([]map[string]ddbtypes.AttributeValue, 0, len(projects))
	for _, project := range projects {
		entries = append(entries, item(projectsPartition, naming.ProjectKey(project)))
	}
	return [][]map[string]ddbtypes.AttributeValue{entries}
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
	ddb := &recordingDynamo{pages: stackPages("shop", want[:2], want[2:])}
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

	stacks := &recordingDynamo{pages: stackPages("shop", []naming.StackName{naming.InfraStack("prod")})}
	if _, err := (&Index{Dynamo: stacks, Table: "state"}).Stacks(context.Background(), "shop"); err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	projects := &recordingDynamo{pages: projectPage("shop")}
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

	ddb := &recordingDynamo{pages: projectPage("billing", "shop")}
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

	ddb := &recordingDynamo{pages: [][]map[string]ddbtypes.AttributeValue{{
		{sortAttribute: &ddbtypes.AttributeValueMemberS{Value: "PROJECT#shop#STACK#prod--web"}},
	}}}
	if _, err := (&Index{Dynamo: ddb, Table: "state"}).Stacks(context.Background(), "shop"); err == nil {
		t.Fatal("Stacks over an unparseable entry err = nil, want the teardown told rather than silently short")
	}
}
