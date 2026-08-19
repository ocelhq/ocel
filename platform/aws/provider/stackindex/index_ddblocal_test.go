package stackindex

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestIndexAgainstDynamoDBLocal(t *testing.T) {
	endpoint := os.Getenv("OCEL_RUNTIME_DDB_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	ctx := context.Background()
	ddb := dynamodb.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := ddb.ListTables(probeCtx, &dynamodb.ListTablesInput{}); err != nil {
		t.Skipf("dynamodb-local not reachable at %s (run `docker compose up -d dynamodb`): %v", endpoint, err)
	}

	table := "ocel-stackindex-ddblocal-" + strings.ReplaceAll(t.Name(), "/", "_")
	createTable(t, ctx, ddb, table)
	ix := &Index{Dynamo: ddb, Table: table}

	t.Run("concurrent deploys converge on one entry", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make([]error, 50)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := ix.AddProject(ctx, "shop", nil); err != nil {
					errs[i] = err
					return
				}
				errs[i] = ix.AddStack(ctx, "shop", naming.AppStack("prod", "web", naming.NewRelease(fmt.Sprintf("b%d", i), "")))
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("concurrent deploy %d: %v", i, err)
			}
		}

		projects, err := ix.Projects(ctx)
		if err != nil {
			t.Fatalf("Projects: %v", err)
		}
		if !reflect.DeepEqual(projects, []string{"shop"}) {
			t.Fatalf("Projects = %v, want one entry per project however many deploys raced", projects)
		}

		stacks, err := ix.Stacks(ctx, "shop")
		if err != nil {
			t.Fatalf("Stacks: %v", err)
		}
		if len(stacks) != len(errs) {
			t.Fatalf("Stacks = %d entries, want %d — every racing deploy is indexed", len(stacks), len(errs))
		}
	})

	t.Run("another project's stacks stay out of the answer", func(t *testing.T) {
		if err := ix.AddProject(ctx, "billing", nil); err != nil {
			t.Fatalf("AddProject: %v", err)
		}
		if err := ix.AddStack(ctx, "billing", naming.InfraStack("prod")); err != nil {
			t.Fatalf("AddStack: %v", err)
		}

		stacks, err := ix.Stacks(ctx, "billing")
		if err != nil {
			t.Fatalf("Stacks: %v", err)
		}
		if !reflect.DeepEqual(stacks, []naming.StackName{naming.InfraStack("prod")}) {
			t.Fatalf("Stacks = %v, want billing's own", stacks)
		}
	})

	t.Run("teardown leaves neither stack nor project behind", func(t *testing.T) {
		clocked := naming.AppStack("prod", "web", naming.NewRelease("clock", ""))
		if err := ix.AddStack(ctx, "billing", clocked); err != nil {
			t.Fatalf("AddStack: %v", err)
		}
		for _, tag := range []string{"products", "carts"} {
			writeTagClock(t, ctx, ddb, table, "billing", clocked, tag)
		}
		if err := ix.RemoveStack(ctx, "billing", clocked); err != nil {
			t.Fatalf("RemoveStack: %v", err)
		}
		if left := tagClockRows(t, ctx, ddb, table, "billing", clocked); len(left) != 0 {
			t.Fatalf("tag clock rows %v survived the stack that wrote them", left)
		}

		if err := ix.RemoveStack(ctx, "billing", naming.InfraStack("prod")); err != nil {
			t.Fatalf("RemoveStack: %v", err)
		}
		if err := ix.RemoveStack(ctx, "billing", naming.InfraStack("prod")); err != nil {
			t.Fatalf("repeated RemoveStack: %v", err)
		}
		if err := ix.RemoveProject(ctx, "billing"); err != nil {
			t.Fatalf("RemoveProject: %v", err)
		}

		stacks, err := ix.Stacks(ctx, "billing")
		if err != nil {
			t.Fatalf("Stacks: %v", err)
		}
		if len(stacks) != 0 {
			t.Fatalf("Stacks = %v after teardown, want nothing", stacks)
		}
		projects, err := ix.Projects(ctx)
		if err != nil {
			t.Fatalf("Projects: %v", err)
		}
		if !reflect.DeepEqual(projects, []string{"shop"}) {
			t.Fatalf("Projects = %v, want the torn-down project gone", projects)
		}
	})
}

func writeTagClock(t *testing.T, ctx context.Context, ddb *dynamodb.Client, table, project string, stack naming.StackName, tag string) {
	t.Helper()
	if _, err := ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item: map[string]ddbtypes.AttributeValue{
			"pk":     &ddbtypes.AttributeValueMemberS{Value: naming.ISRTagKey(project, stack, tag)},
			"sk":     &ddbtypes.AttributeValueMemberS{Value: tagSortKey},
			"gsi1pk": &ddbtypes.AttributeValueMemberS{Value: naming.ISRTagPrefix(project, stack)},
			"gsi1sk": &ddbtypes.AttributeValueMemberS{Value: "000001700000000"},
			"tag":    &ddbtypes.AttributeValueMemberS{Value: tag},
		},
	}); err != nil {
		t.Fatalf("write tag clock %s: %v", tag, err)
	}
}

func tagClockRows(t *testing.T, ctx context.Context, ddb *dynamodb.Client, table, project string, stack naming.StackName) []string {
	t.Helper()
	rows, err := (&Index{Dynamo: ddb, Table: table}).tagPartitions(ctx, naming.ISRTagPrefix(project, stack))
	if err != nil {
		t.Fatalf("read tag clock rows: %v", err)
	}
	return rows
}

func createTable(t *testing.T, ctx context.Context, ddb *dynamodb.Client, table string) {
	t.Helper()
	if _, err := ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: ddbtypes.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []ddbtypes.GlobalSecondaryIndex{{
			IndexName: aws.String(IndexName),
			KeySchema: []ddbtypes.KeySchemaElement{
				{AttributeName: aws.String("gsi1pk"), KeyType: ddbtypes.KeyTypeHash},
				{AttributeName: aws.String("gsi1sk"), KeyType: ddbtypes.KeyTypeRange},
			},
			Projection: &ddbtypes.Projection{
				ProjectionType:   ddbtypes.ProjectionTypeInclude,
				NonKeyAttributes: []string{"expired", "stale", "tag"},
			},
		}},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ddb.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	for range 20 {
		out, err := ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
		if err == nil && out.Table.TableStatus == ddbtypes.TableStatusActive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
