package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TestMarkSucceeded_RealDDBIsAtomicAndIdempotent exercises the store's guarded
// pending->succeeded transition against a real DynamoDB (dynamodb-local), which
// the in-memory fake cannot: only a live engine validates the actual
// UpdateExpression / ConditionExpression the listener relies on for idempotency.
//
// It self-skips when dynamodb-local is unreachable. Run it with:
//
//	docker compose up -d dynamodb
//	go test ./cloud/aws/runtime -run TestMarkSucceeded_RealDDBIsAtomicAndIdempotent
func TestMarkSucceeded_RealDDBIsAtomicAndIdempotent(t *testing.T) {
	endpoint := os.Getenv("OCEL_RUNTIME_DDB_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000"
	}
	region := "us-east-1"
	creds := credentials.NewStaticCredentialsProvider("test", "test", "")
	ctx := context.Background()
	ddb := dynamodb.NewFromConfig(aws.Config{Region: region, Credentials: creds}, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := ddb.ListTables(probeCtx, &dynamodb.ListTablesInput{}); err != nil {
		t.Skipf("dynamodb-local not reachable at %s (run `docker compose up -d dynamodb`): %v", endpoint, err)
	}

	table := "ocel-listener-ddblocal-" + strings.ReplaceAll(t.Name(), "/", "_")
	createTable(t, ctx, ddb, table)

	store := &sessionStore{client: ddb, table: table}
	sess := session{
		SessionID:       "sess_ddblocal",
		Secret:          "secret",
		Bucket:          "b",
		CallbackBaseURL: "https://app.example.com/api/upload",
		Metadata:        []byte(`{}`),
		Files: []sessionFile{
			{Key: "a.png", Name: "a.png", Size: 1, MimeType: "image/png", State: statePending},
			{Key: "b.png", Name: "b.png", Size: 2, MimeType: "image/png", State: statePending},
		},
		CreatedAt: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := store.put(ctx, sess); err != nil {
		t.Fatalf("put session: %v", err)
	}

	// First transition of file 1 succeeds; a duplicate delivery no-ops.
	first, err := store.markSucceeded(ctx, sess.SessionID, 1)
	if err != nil {
		t.Fatalf("first markSucceeded: %v", err)
	}
	if !first {
		t.Fatal("first transition must report transitioned = true")
	}
	dup, err := store.markSucceeded(ctx, sess.SessionID, 1)
	if err != nil {
		t.Fatalf("duplicate markSucceeded: %v", err)
	}
	if dup {
		t.Fatal("duplicate transition must report transitioned = false (idempotent)")
	}

	// Only file 1 flipped; file 0 stays pending — the guard is per-file-index.
	got, err := store.get(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Files[1].State != stateSucceeded {
		t.Fatalf("file[1] state = %q, want succeeded", got.Files[1].State)
	}
	if got.Files[0].State != statePending {
		t.Fatalf("file[0] state = %q, want pending (untouched)", got.Files[0].State)
	}
}

func createTable(t *testing.T, ctx context.Context, ddb *dynamodb.Client, table string) {
	t.Helper()
	if _, err := ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("session_id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("session_id"), KeyType: ddbtypes.KeyTypeHash},
		},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ddb.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})
	for i := 0; i < 20; i++ {
		out, err := ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
		if err == nil && out.Table.TableStatus == ddbtypes.TableStatusActive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
