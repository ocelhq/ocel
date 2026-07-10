package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	runtimev1 "github.com/ocelhq/ocel/pkg/proto/runtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/runtime/v1/runtimev1connect"
)

// TestRuntimeDirectDial builds the real ocelawsrt binary, spawns it against a
// local DynamoDB, dials its RuntimeService over the private socket, and asserts
// PresignUpload returns a real presigned PUT (bound content-length/content-type
// + session tag) and writes a pending session to DynamoDB.
//
// It requires dynamodb-local. Run it with:
//
//	docker compose up -d dynamodb
//	go test ./cloud/aws/cmd/ocelawsrt -run TestRuntimeDirectDial
//
// It self-skips when dynamodb-local is not reachable (mirrors the MinIO dev
// e2e's gate), so default CI without the container is unaffected.
func TestRuntimeDirectDial(t *testing.T) {
	ddbEndpoint := os.Getenv(ddbEndpointEnvVar)
	if ddbEndpoint == "" {
		ddbEndpoint = "http://localhost:8000"
	}
	region := "us-east-1"
	creds := credentials.NewStaticCredentialsProvider("test", "test", "")

	ctx := context.Background()
	ddb := dynamodb.NewFromConfig(aws.Config{Region: region, Credentials: creds}, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(ddbEndpoint)
	})

	// Gate: skip unless dynamodb-local answers.
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := ddb.ListTables(probeCtx, &dynamodb.ListTablesInput{}); err != nil {
		t.Skipf("dynamodb-local not reachable at %s (run `docker compose up -d dynamodb`): %v", ddbEndpoint, err)
	}

	table := "ocel-runtime-e2e-" + strings.ReplaceAll(t.Name(), "/", "_")
	createSessionsTable(t, ctx, ddb, table)

	bucket := "prod-e2e-bucket"
	token := "e2e-session-token"

	binPath := buildRuntimeBinary(t)

	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		providerv1.SessionTokenEnvVar+"="+token,
		tableEnvVar+"="+table,
		bucketEnvVar+"="+bucket,
		regionEnvVar+"="+region,
		ddbEndpointEnvVar+"="+ddbEndpoint,
		s3EndpointEnvVar+"=http://localhost:9000",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION="+region,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start runtime binary: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	addr := readReadiness(t, stdout)
	client := dialRuntime(t, addr, token)

	resp, err := client.PresignUpload(ctx, &runtimev1.PresignUploadRequest{
		Bucket:          "storage",
		CallbackBaseUrl: "https://app.example/api/blob",
		Metadata:        []byte(`{"user":"u1"}`),
		Files: []*runtimev1.PresignFile{
			{Key: "avatars/u1.png", Name: "u1.png", Size: 2048, MimeType: "image/png"},
		},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	if resp.GetSessionId() == "" {
		t.Fatal("expected a session id")
	}
	if len(resp.GetFiles()) != 1 {
		t.Fatalf("targets = %d, want 1", len(resp.GetFiles()))
	}

	url := resp.GetFiles()[0].GetUrl()
	if !strings.Contains(url, bucket+"/avatars/u1.png") {
		t.Fatalf("url does not target the prod bucket + as-is key: %s", url)
	}
	// The presigned PUT binds exact content-length and content-type and a
	// SigV4-signed x-amz-tagging session id (all appear in X-Amz-SignedHeaders).
	for _, want := range []string{"X-Amz-Signature=", "content-length", "content-type", "x-amz-tagging"} {
		if !strings.Contains(url, want) {
			t.Fatalf("presigned url missing %q: %s", want, url)
		}
	}

	// The pending session landed in DynamoDB with the provisioned table schema.
	item := getSession(t, ctx, ddb, table, resp.GetSessionId())
	if item["secret"] == nil || avS(item["secret"]) == "" {
		t.Fatal("session must persist a minted secret")
	}
	if got := avS(item["bucket"]); got != bucket {
		t.Fatalf("session bucket = %q, want %q", got, bucket)
	}
	if got := avS(item["callback_base_url"]); got != "https://app.example/api/blob" {
		t.Fatalf("callback_base_url = %q", got)
	}
	if item["expires_at"] == nil {
		t.Fatal("session must carry the expires_at TTL attribute")
	}
	files, ok := item["files"].(*ddbtypes.AttributeValueMemberL)
	if !ok || len(files.Value) != 1 {
		t.Fatalf("session must persist one file record: %#v", item["files"])
	}
	fileRec := files.Value[0].(*ddbtypes.AttributeValueMemberM).Value
	if got := avS(fileRec["state"]); got != "pending" {
		t.Fatalf("file state = %q, want pending", got)
	}
}

func createSessionsTable(t *testing.T, ctx context.Context, ddb *dynamodb.Client, table string) {
	t.Helper()
	_, err := ddb.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("session_id"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("session_id"), KeyType: ddbtypes.KeyTypeHash},
		},
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = ddb.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})

	// dynamodb-local returns tables ACTIVE immediately, but be defensive.
	for i := 0; i < 20; i++ {
		out, err := ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
		if err == nil && out.Table.TableStatus == ddbtypes.TableStatusActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if _, err := ddb.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(table),
		TimeToLiveSpecification: &ddbtypes.TimeToLiveSpecification{
			AttributeName: aws.String("expires_at"),
			Enabled:       aws.Bool(true),
		},
	}); err != nil {
		// dynamodb-local supports TTL config; a failure here is a real problem.
		t.Fatalf("enable TTL on expires_at: %v", err)
	}
}

func getSession(t *testing.T, ctx context.Context, ddb *dynamodb.Client, table, sessionID string) map[string]ddbtypes.AttributeValue {
	t.Helper()
	out, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]ddbtypes.AttributeValue{
			"session_id": &ddbtypes.AttributeValueMemberS{Value: sessionID},
		},
	})
	if err != nil {
		t.Fatalf("get session item: %v", err)
	}
	if len(out.Item) == 0 {
		t.Fatalf("no session item written for %s", sessionID)
	}
	return out.Item
}

func avS(v ddbtypes.AttributeValue) string {
	s, ok := v.(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return s.Value
}

func buildRuntimeBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found on PATH")
	}
	out := filepath.Join(t.TempDir(), "ocelawsrt")
	build := exec.Command("go", "build", "-o", out, ".")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ocelawsrt: %v\n%s", err, b)
	}
	return out
}

func readReadiness(t *testing.T, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			if addr, ok := providerv1.ParseReadinessLine(scanner.Text()); ok {
				ch <- result{addr: addr}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- result{err: err}
			return
		}
		ch <- result{err: errors.New("runtime exited before printing readiness")}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("await readiness: %v", res.err)
		}
		return res.addr
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for readiness sentinel")
		return ""
	}
}

func dialRuntime(t *testing.T, addr, token string) runtimev1connect.RuntimeServiceClient {
	t.Helper()
	network, address, err := providerv1.ParseAddr(addr)
	if err != nil {
		t.Fatalf("parse readiness addr: %v", err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, address)
			},
		},
	}
	return runtimev1connect.NewRuntimeServiceClient(
		httpClient,
		"http://runtime",
		connect.WithInterceptors(&clientAuth{token: token}),
	)
}

// clientAuth presents the per-session token on every RPC, mirroring the
// server's expectation.
type clientAuth struct{ token string }

func (c *clientAuth) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", providerv1.FormatAuthHeader(c.token))
		return next(ctx, req)
	}
}

func (c *clientAuth) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", providerv1.FormatAuthHeader(c.token))
		return conn
	}
}

func (c *clientAuth) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
