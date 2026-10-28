package sdkconfig

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestControlRetryerIsAdaptiveAndWiderThanTheDefault(t *testing.T) {
	r := ControlRetryer()
	if _, ok := r.(*retry.AdaptiveMode); !ok {
		t.Fatalf("ControlRetryer() = %T, want *retry.AdaptiveMode", r)
	}
	if got := r.MaxAttempts(); got != controlMaxAttempts {
		t.Fatalf("MaxAttempts() = %d, want %d", got, controlMaxAttempts)
	}
}

func TestControl(t *testing.T) {
	t.Run("the config includes its retryer and region", func(t *testing.T) {
		t.Setenv("AWS_REGION", "us-east-1")

		control, err := Control(context.Background(), "eu-west-1")
		if err != nil {
			t.Fatalf("Control: %v", err)
		}
		if control.Region != "eu-west-1" {
			t.Fatalf("Region = %q, want eu-west-1", control.Region)
		}
		if control.Retryer == nil {
			t.Fatal("Control config has no retryer")
		}
		if got := control.Retryer().MaxAttempts(); got != controlMaxAttempts {
			t.Fatalf("control MaxAttempts() = %d, want %d", got, controlMaxAttempts)
		}
	})

	t.Run("with no region it falls back to the ambient chain", func(t *testing.T) {
		t.Setenv("AWS_REGION", "ap-southeast-2")

		cfg, err := Control(context.Background(), "")
		if err != nil {
			t.Fatalf("Control: %v", err)
		}
		if cfg.Region != "ap-southeast-2" {
			t.Fatalf("Region = %q, want ap-southeast-2", cfg.Region)
		}
	})
}

func controlAgainst(t *testing.T, handler http.HandlerFunc) aws.Config {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	staticCredentials(t, t.TempDir(), server.URL)
	cfg, err := Control(context.Background(), "eu-west-1")
	if err != nil {
		t.Fatalf("Control: %v", err)
	}
	return cfg
}

func staticCredentials(t *testing.T, dir, endpoint string) {
	t.Helper()
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "example-secret")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

const ssoAccessToken = "sso-cached-access-token"

func ssoProfile(t *testing.T, endpoint string) {
	t.Helper()
	home := t.TempDir()
	startURL := "https://ocel.awsapps.com/start"
	t.Setenv("HOME", home)
	t.Setenv("AWS_ENDPOINT_URL_SSO", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_PROFILE", "ocel")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	profile := "[profile ocel]\nsso_start_url = " + startURL + "\nsso_region = eu-west-1\nsso_account_id = 111111111111\nsso_role_name = web\n"
	if err := os.WriteFile(filepath.Join(home, "config"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, ".aws", "sso", "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(startURL))
	token := `{"accessToken":"` + ssoAccessToken + `","expiresAt":"2099-01-01T00:00:00Z","region":"eu-west-1","startUrl":"` + startURL + `"}`
	if err := os.WriteFile(filepath.Join(cache, hex.EncodeToString(sum[:])+".json"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadingCredentialsIsTraced(t *testing.T) {
	t.Run("an SSO role credential exchange is a span that keeps no body when refused", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		refusal := `{"message":"Session token not found or invalid"}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Amzn-Errortype", "UnauthorizedException")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, refusal)
		}))
		t.Cleanup(server.Close)
		ssoProfile(t, server.URL)

		cfg, err := Control(ctx, "eu-west-1")
		if err != nil {
			t.Fatalf("Control: %v", err)
		}
		if _, err := dynamodb.NewFromConfig(cfg).ListTables(ctx, &dynamodb.ListTablesInput{}); err == nil {
			t.Fatal("ListTables succeeded with refused credentials")
		}

		spans := recorded.Ended()
		operation := tracingtest.SpanNamed(t, spans, "SSO/GetRoleCredentials")
		if got := tracingtest.Attributes(operation)["error.type"].AsString(); got != "UnauthorizedException" {
			t.Errorf("error.type = %q, want UnauthorizedException", got)
		}
		var exchange sdktrace.ReadOnlySpan
		for _, child := range tracingtest.ChildrenOf(spans, operation) {
			if child.InstrumentationScope().Name == tracing.Scope {
				exchange = child
			}
		}
		if exchange == nil {
			t.Fatal("SSO/GetRoleCredentials has no HTTP span")
		}
		attrs := tracingtest.Attributes(exchange)
		if got := attrs["http.response.status_code"].AsInt64(); got != http.StatusForbidden {
			t.Errorf("http.response.status_code = %d, want 403", got)
		}
		if got := attrs["http.response.body.size"].AsInt64(); got != int64(len(refusal)) {
			t.Errorf("http.response.body.size = %d, want %d", got, len(refusal))
		}
		if body, recordedBody := attrs["http.response.body"]; recordedBody {
			t.Errorf("http.response.body = %q, want a credential exchange's body kept out", body.AsString())
		}
		for _, s := range spans {
			if exported := tracingtest.ExportedText(s); strings.Contains(exported, ssoAccessToken) {
				t.Errorf("span %q carries the SSO access token:\n%s", s.Name(), exported)
			}
		}
	})

	t.Run("a custom CA bundle still verifies the endpoint, and the call is traced", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			_, _ = io.WriteString(w, `{}`)
		}))
		t.Cleanup(server.Close)
		dir := t.TempDir()
		bundle := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		staticCredentials(t, dir, server.URL)
		t.Setenv("AWS_CA_BUNDLE", bundle)

		cfg, err := Control(ctx, "eu-west-1")
		if err != nil {
			t.Fatalf("Control: %v", err)
		}
		if _, err := dynamodb.NewFromConfig(cfg).ListTables(ctx, &dynamodb.ListTablesInput{}); err != nil {
			t.Fatalf("ListTables through the CA bundle: %v", err)
		}
		for _, s := range recorded.Ended() {
			if s.InstrumentationScope().Name == tracing.Scope {
				return
			}
		}
		t.Error("no HTTP span from the tracing transport")
	})
}

func TestOnlyTheSDKRefusingATracedClientUnderACABundleFallsBackToTheSDKsOwnClient(t *testing.T) {
	t.Run("a CA bundle the SDK can load only into its own client", func(t *testing.T) {
		dir := t.TempDir()
		bundle := filepath.Join(dir, "ca.pem")
		server := httptest.NewTLSServer(http.NotFoundHandler())
		t.Cleanup(server.Close)
		if err := os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		staticCredentials(t, dir, server.URL)
		t.Setenv("AWS_CA_BUNDLE", bundle)

		_, err := config.LoadDefaultConfig(context.Background(), config.WithHTTPClient(NewTracedClient(nil)))
		if !refusesTracedClient(err) {
			t.Errorf("refusesTracedClient(%v) = false, want the CA bundle's refusal recognised", err)
		}
	})

	t.Run("a profile that does not exist", func(t *testing.T) {
		staticCredentials(t, t.TempDir(), "https://aws.invalid")
		t.Setenv("AWS_PROFILE", "missing")

		_, err := config.LoadDefaultConfig(context.Background(), config.WithHTTPClient(NewTracedClient(nil)))
		if err == nil {
			t.Fatal("LoadDefaultConfig() = nil, want the missing profile refused")
		}
		if refusesTracedClient(err) {
			t.Errorf("refusesTracedClient(%v) = true, want only the CA bundle's refusal to fall back", err)
		}
		if _, err := Control(context.Background(), "eu-west-1"); err == nil {
			t.Error("Control() = nil, want the missing profile refused")
		}
	})
}

func TestControlCallsAreSpans(t *testing.T) {
	t.Run("a DynamoDB read names its table and key, and its item never reaches the trace", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		cfg := controlAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			w.Header().Set("X-Amzn-Requestid", "req-dynamo")
			_, _ = io.WriteString(w, `{"Item":{"value":{"S":"plaintext-variable"}}}`)
		})

		_, err := dynamodb.NewFromConfig(cfg).GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String("ocel-records"),
			Key: map[string]ddbtypes.AttributeValue{
				"pk": &ddbtypes.AttributeValueMemberS{Value: "production#web"},
				"sk": &ddbtypes.AttributeValueMemberS{Value: "release#"},
			},
		})
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}

		attrs := tracingtest.Attributes(tracingtest.SpanNamed(t, recorded.Ended(), "DynamoDB/GetItem"))
		if got := attrs["aws.dynamodb.table_names"].AsStringSlice(); !slices.Equal(got, []string{"ocel-records"}) {
			t.Errorf("aws.dynamodb.table_names = %v, want [ocel-records]", got)
		}
		if got := attrs["aws.dynamodb.key.pk"].AsString(); got != "production#web" {
			t.Errorf("aws.dynamodb.key.pk = %q, want production#web", got)
		}
		if got := attrs["aws.dynamodb.key.sk"].AsString(); got != "release#" {
			t.Errorf("aws.dynamodb.key.sk = %q, want release#", got)
		}
		if got := attrs["aws.request_id"].AsString(); got != "req-dynamo" {
			t.Errorf("aws.request_id = %q, want req-dynamo", got)
		}
		if status, ok := attrs["http.response.status_code"]; ok {
			t.Errorf("http.response.status_code = %d on the operation, want it only on its HTTP span", status.AsInt64())
		}
		for _, s := range recorded.Ended() {
			for _, keyValue := range s.Attributes() {
				if strings.Contains(keyValue.Value.String(), "plaintext-variable") {
					t.Errorf("span %q attribute %s carries the item's value", s.Name(), keyValue.Key)
				}
			}
		}
	})

	t.Run("an SSM read's HTTP exchange is a span with its status, and the decrypted value reaches no span", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		cfg := controlAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			_, _ = io.WriteString(w, `{"Parameter":{"Name":"/ocel/db","Type":"SecureString","Value":"decrypted-database-url"}}`)
		})

		_, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
			Name:           aws.String("/ocel/db"),
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			t.Fatalf("GetParameter: %v", err)
		}

		var exchange sdktrace.ReadOnlySpan
		for _, s := range recorded.Ended() {
			if s.InstrumentationScope().Name == tracing.Scope {
				exchange = s
			}
			for _, keyValue := range s.Attributes() {
				if strings.Contains(keyValue.Value.String(), "decrypted-database-url") {
					t.Errorf("span %q attribute %s carries the decrypted value", s.Name(), keyValue.Key)
				}
			}
		}
		if exchange == nil {
			t.Fatal("no HTTP span from the tracing transport")
		}
		if got := tracingtest.Attributes(exchange)["http.response.status_code"].AsInt64(); got != http.StatusOK {
			t.Errorf("http.response.status_code = %d, want 200", got)
		}
	})

	t.Run("a DynamoDB transaction names every table it writes", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		cfg := controlAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-amz-json-1.0")
			_, _ = io.WriteString(w, `{}`)
		})

		_, err := dynamodb.NewFromConfig(cfg).TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
			TransactItems: []ddbtypes.TransactWriteItem{
				{Put: &ddbtypes.Put{TableName: aws.String("ocel-records"), Item: map[string]ddbtypes.AttributeValue{"pk": &ddbtypes.AttributeValueMemberS{Value: "a"}}}},
				{Delete: &ddbtypes.Delete{TableName: aws.String("ocel-values"), Key: map[string]ddbtypes.AttributeValue{"pk": &ddbtypes.AttributeValueMemberS{Value: "b"}}}},
			},
		})
		if err != nil {
			t.Fatalf("TransactWriteItems: %v", err)
		}

		attrs := tracingtest.Attributes(tracingtest.SpanNamed(t, recorded.Ended(), "DynamoDB/TransactWriteItems"))
		if got := attrs["aws.dynamodb.table_names"].AsStringSlice(); !slices.Equal(got, []string{"ocel-records", "ocel-values"}) {
			t.Errorf("aws.dynamodb.table_names = %v, want [ocel-records ocel-values]", got)
		}
	})

	t.Run("an S3 object read names its bucket and key", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		cfg := controlAgainst(t, func(w http.ResponseWriter, r *http.Request) {})

		_, err := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }).HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String("ocel-artifacts"),
			Key:    aws.String("production/web/abc/hosting.json"),
		})
		if err != nil {
			t.Fatalf("HeadObject: %v", err)
		}

		attrs := tracingtest.Attributes(tracingtest.SpanNamed(t, recorded.Ended(), "S3/HeadObject"))
		if got := attrs["aws.s3.bucket"].AsString(); got != "ocel-artifacts" {
			t.Errorf("aws.s3.bucket = %q, want ocel-artifacts", got)
		}
		if got := attrs["aws.s3.key"].AsString(); got != "production/web/abc/hosting.json" {
			t.Errorf("aws.s3.key = %q, want production/web/abc/hosting.json", got)
		}
	})

	t.Run("a call's HTTP exchange nests under its operation, under the caller's span", func(t *testing.T) {
		ctx, recorded := tracingtest.Record(t)
		cfg := controlAgainst(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/xml")
			_, _ = io.WriteString(w, `<DescribeStacksResponse><DescribeStacksResult><Stacks></Stacks></DescribeStacksResult></DescribeStacksResponse>`)
		})

		ctx, caller := otel.Tracer("test").Start(ctx, "deploy")
		_, err := cloudformation.NewFromConfig(cfg).DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String("ocel-bootstrap")})
		caller.End()
		if err != nil {
			t.Fatalf("DescribeStacks: %v", err)
		}

		byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
		var exchange sdktrace.ReadOnlySpan
		for _, s := range recorded.Ended() {
			byID[s.SpanContext().SpanID()] = s
			if s.InstrumentationScope().Name == tracing.Scope {
				exchange = s
			}
		}
		if exchange == nil {
			t.Fatal("no HTTP span from the tracing transport")
		}
		if got := tracingtest.Attributes(exchange)["http.response.status_code"].AsInt64(); got != http.StatusOK {
			t.Errorf("http.response.status_code = %d, want the response's", got)
		}
		var ancestors []string
		for s := exchange; s.Parent().IsValid(); {
			parent, ok := byID[s.Parent().SpanID()]
			if !ok {
				break
			}
			ancestors = append(ancestors, parent.Name())
			s = parent
		}
		if !slices.Contains(ancestors, "CloudFormation/DescribeStacks") || ancestors[len(ancestors)-1] != "deploy" {
			t.Errorf("HTTP span ancestors = %v, want CloudFormation/DescribeStacks under deploy", ancestors)
		}
	})
}
