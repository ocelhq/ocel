package stackindex

import (
	"context"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type pagingDynamo struct {
	pages   [][]string
	queries []*dynamodb.QueryInput
}

func (p *pagingDynamo) PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

func (p *pagingDynamo) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}

func (p *pagingDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	p.queries = append(p.queries, in)
	page := p.pages[len(p.queries)-1]
	out := &dynamodb.QueryOutput{}
	for _, sk := range page {
		out.Items = append(out.Items, key(aws.ToString(in.TableName), sk))
	}
	if len(p.queries) < len(p.pages) {
		out.LastEvaluatedKey = key("more", page[len(page)-1])
	}
	return out, nil
}

func TestStacksPagesToTheEnd(t *testing.T) {
	t.Parallel()

	ddb := &pagingDynamo{pages: [][]string{{"shop--infra", "shop--web--b1"}, {"shop--web--b2"}}}
	ix := &Index{Dynamo: ddb, Table: "state"}

	got, err := ix.Stacks(context.Background(), "shop")
	if err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	want := []string{"shop--infra", "shop--web--b1", "shop--web--b2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Stacks = %v, want %v", got, want)
	}
	if len(ddb.queries) != 2 {
		t.Fatalf("issued %d queries, want the second page fetched", len(ddb.queries))
	}
	for i, q := range ddb.queries {
		pk, ok := q.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
		if !ok || pk.Value != scopePartition+"shop" {
			t.Fatalf("query %d partition = %v, want the project's own", i, q.ExpressionAttributeValues[":pk"])
		}
	}
}

func TestReadsAreConsistent(t *testing.T) {
	t.Parallel()

	stacks := &pagingDynamo{pages: [][]string{{"shop--infra"}}}
	if _, err := (&Index{Dynamo: stacks, Table: "state"}).Stacks(context.Background(), "shop"); err != nil {
		t.Fatalf("Stacks: %v", err)
	}
	projects := &pagingDynamo{pages: [][]string{{"shop"}}}
	if _, err := (&Index{Dynamo: projects, Table: "state"}).Projects(context.Background()); err != nil {
		t.Fatalf("Projects: %v", err)
	}
	for _, ddb := range []*pagingDynamo{stacks, projects} {
		for i, q := range ddb.queries {
			if !aws.ToBool(q.ConsistentRead) {
				t.Errorf("query %d read eventually: a teardown reads back what it just wrote", i)
			}
		}
	}
}

func TestProjectsAndStacksLiveInDifferentPartitions(t *testing.T) {
	t.Parallel()

	ddb := &pagingDynamo{pages: [][]string{{"billing", "shop"}}}
	ix := &Index{Dynamo: ddb, Table: "state"}

	got, err := ix.Projects(context.Background())
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if want := []string{"billing", "shop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Projects = %v, want %v", got, want)
	}
	pk := ddb.queries[0].ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if pk.Value != projectsPartition {
		t.Fatalf("Projects queried %q, want %q", pk.Value, projectsPartition)
	}
}

func TestScopeOf(t *testing.T) {
	t.Parallel()

	if got, err := ScopeOf("shop--preview-pr-1--web--b1"); err != nil || got != "shop" {
		t.Fatalf("ScopeOf = %q, %v, want shop", got, err)
	}
	if _, err := ScopeOf("stray"); err == nil {
		t.Fatal("ScopeOf on a name carrying no scope err = nil, want the name refused")
	}
	if _, err := ScopeOf("--infra"); err == nil {
		t.Fatal("ScopeOf on an empty scope err = nil, want the name refused")
	}
}
