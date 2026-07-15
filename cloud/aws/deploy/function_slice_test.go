package deploy

import (
	"encoding/json"
	"slices"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestTranslateFunction_PassesRuntimeAndEntrypoint(t *testing.T) {
	got := translateFunction(&deploymentsv1.ManifestFunction{
		Runtime: "nodejs24.x",
		Handler: "src/server.js",
	})
	if got.Runtime != "nodejs24.x" {
		t.Errorf("Runtime = %q, want nodejs24.x", got.Runtime)
	}
	if got.Handler != "src/server.js" {
		t.Errorf("Handler = %q, want src/server.js", got.Handler)
	}
}

func TestTranslateFunction_EmptyFallsBackToPinnedDefaults(t *testing.T) {
	got := translateFunction(&deploymentsv1.ManifestFunction{})
	if got.Runtime != defaultFunctionRuntime {
		t.Errorf("Runtime = %q, want default %q", got.Runtime, defaultFunctionRuntime)
	}
	if got.Handler != defaultFunctionEntry {
		t.Errorf("Handler = %q, want default %q", got.Handler, defaultFunctionEntry)
	}
}

func TestMembraneLayerARN_DefaultAndEnvOverride(t *testing.T) {
	t.Setenv(membraneLayerARNEnv, "")
	if got := membraneLayerARN(); got != defaultMembraneLayerARN {
		t.Errorf("membraneLayerARN() = %q, want default %q", got, defaultMembraneLayerARN)
	}
	t.Setenv(membraneLayerARNEnv, "arn:aws:lambda:us-east-1:123:layer:ocel-membrane:9")
	if got := membraneLayerARN(); got != "arn:aws:lambda:us-east-1:123:layer:ocel-membrane:9" {
		t.Errorf("membraneLayerARN() = %q, want the env override", got)
	}
}

func TestFunctionEnvKey_UsesCanonicalTypeTokenAndUserID(t *testing.T) {
	if got := functionEnvKey(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "main"); got != "OCEL_RESOURCE_POSTGRES_main" {
		t.Errorf("functionEnvKey(postgres, main) = %q, want OCEL_RESOURCE_POSTGRES_main", got)
	}
	if got := functionEnvKey(resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, "uploads"); got != "OCEL_RESOURCE_BUCKET_uploads" {
		t.Errorf("functionEnvKey(bucket, uploads) = %q, want OCEL_RESOURCE_BUCKET_uploads", got)
	}
}

func TestPostgresEnvPayload_MatchesSDKConnectionStringShape(t *testing.T) {
	payload := postgresEnvPayload("ocel", "s3cr3t", "db.host", 5432, "ocel")
	var parsed struct {
		ConnectionString string `json:"connectionString"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	want := "postgres://ocel:s3cr3t@db.host:5432/ocel"
	if parsed.ConnectionString != want {
		t.Errorf("connectionString = %q, want %q", parsed.ConnectionString, want)
	}
}

func TestBucketEnvPayload_MatchesSDKAddressBucketShape(t *testing.T) {
	payload := bucketEnvPayload("unix:///run/ocel/runtime.sock", "my-bucket-abc123")
	var parsed struct {
		Address string `json:"address"`
		Bucket  string `json:"bucket"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if parsed.Address != "unix:///run/ocel/runtime.sock" {
		t.Errorf("address = %q, want the BucketService endpoint", parsed.Address)
	}
	if parsed.Bucket != "my-bucket-abc123" {
		t.Errorf("bucket = %q, want the provisioned bucket binding", parsed.Bucket)
	}
}

func TestArtifactArchivePath_ResolvesRelativeToOutputRoot(t *testing.T) {
	got := artifactArchivePath("/proj/.ocel/output", "functions/api.func")
	want := "/proj/.ocel/output/functions/api.func"
	if got != want {
		t.Errorf("artifactArchivePath() = %q, want %q", got, want)
	}
}

func TestCollectFunctionOutput_ReportsURLKeyedByLogicalName(t *testing.T) {
	out := collectFunctionOutput("api", "https://abc.lambda-url.us-east-1.on.aws/")
	if out.GetLogicalName() != "api" {
		t.Errorf("LogicalName = %q, want api", out.GetLogicalName())
	}
	fn := out.GetFunction()
	if fn == nil {
		t.Fatal("output has no FunctionOutput; the Function URL must be reported")
	}
	if fn.GetUrl() != "https://abc.lambda-url.us-east-1.on.aws/" {
		t.Errorf("url = %q, want the Function URL", fn.GetUrl())
	}
}

// TestISRPolicy_ScopesToTheAppsOwnNamespace proves a Next function's cache
// grant cannot reach another app's data. The asset bucket and the state table
// are account-global and shared across every env/project/app, and the state
// table also holds upload sessions (whose items carry HMAC secrets) — so an
// unscoped grant here would expose every tenant to every function.
func TestISRPolicy_ScopesToTheAppsOwnNamespace(t *testing.T) {
	cfg := isrConfig{
		Bucket:   "assets-xyz",
		Prefix:   "prod/proj123/marketing/build456",
		Table:    "state-abc",
		TableARN: "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
	}

	raw, err := isrPolicy(cfg)
	if err != nil {
		t.Fatalf("isrPolicy: %v", err)
	}

	var doc struct {
		Statement []struct {
			Effect    string   `json:"Effect"`
			Action    []string `json:"Action"`
			Resource  string   `json:"Resource"`
			Condition map[string]map[string][]string
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("got %d statements, want 2", len(doc.Statement))
	}

	s3Stmt := doc.Statement[0]
	if want := "arn:aws:s3:::assets-xyz/prod/proj123/marketing/build456/*"; s3Stmt.Resource != want {
		t.Errorf("S3 Resource = %q, want %q", s3Stmt.Resource, want)
	}

	ddbStmt := doc.Statement[1]
	if ddbStmt.Resource != cfg.TableARN {
		t.Errorf("DynamoDB Resource = %q, want the table ARN", ddbStmt.Resource)
	}
	// The granted actions must match the calls the handler's tag store actually
	// makes (BatchGetItem to read, UpdateItem to merge). The two live in
	// different languages with nothing linking them, so a missing action is only
	// discovered as a runtime 403 out of the user's revalidateTag call — which is
	// exactly what happened when writeTags moved from PutItem to UpdateItem.
	wantActions := []string{"dynamodb:BatchGetItem", "dynamodb:UpdateItem"}
	if !slices.Equal(ddbStmt.Action, wantActions) {
		t.Errorf("DynamoDB Action = %v, want exactly %v", ddbStmt.Action, wantActions)
	}
	// Exact LeadingKeys matching cannot express a prefix, so the scoping rests
	// on StringLike; a plain StringEquals here would silently grant the table.
	keys := ddbStmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
	if len(keys) != 1 || keys[0] != "TAG#prod#proj123#marketing#build456#*" {
		t.Errorf("LeadingKeys = %v, want the app's own tag partitions", keys)
	}
}

// The handler joins its S3 keys onto OCEL_ISR_PREFIX and its tag partitions onto
// OCEL_ISR_TAG_NAMESPACE. Both must agree with what isrPolicy grants, or every
// read fails closed at runtime.
func TestISREnv_AgreesWithThePolicyScope(t *testing.T) {
	cfg := isrConfig{
		Bucket:   "assets-xyz",
		Prefix:   "prod/proj123/marketing/build456",
		Table:    "state-abc",
		TableARN: "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
	}

	env := cfg.env()

	if env["OCEL_ISR_BUCKET"] != "assets-xyz" {
		t.Errorf("OCEL_ISR_BUCKET = %q", env["OCEL_ISR_BUCKET"])
	}
	if env["OCEL_ISR_PREFIX"] != cfg.Prefix {
		t.Errorf("OCEL_ISR_PREFIX = %q, want %q", env["OCEL_ISR_PREFIX"], cfg.Prefix)
	}
	if env["OCEL_STATE_TABLE"] != "state-abc" {
		t.Errorf("OCEL_STATE_TABLE = %q", env["OCEL_STATE_TABLE"])
	}
	if want := "TAG#prod#proj123#marketing#build456#"; env["OCEL_ISR_TAG_NAMESPACE"] != want {
		t.Errorf("OCEL_ISR_TAG_NAMESPACE = %q, want %q", env["OCEL_ISR_TAG_NAMESPACE"], want)
	}
}
