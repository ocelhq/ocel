package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestTranslateFunction_DefaultsSizeTheFunctionForSSR(t *testing.T) {
	got := translateFunction(&deploymentsv1.ManifestFunction{})
	if got.MemorySizeMB != defaultFunctionMemoryMB {
		t.Errorf("MemorySizeMB = %d, want default %d", got.MemorySizeMB, defaultFunctionMemoryMB)
	}
	if got.TimeoutSeconds != defaultFunctionTimeoutSeconds {
		t.Errorf("TimeoutSeconds = %d, want default %d", got.TimeoutSeconds, defaultFunctionTimeoutSeconds)
	}
}

// Leaving either unset hands the function AWS's implicit 3s/128MB, which a Next
// SSR cold start (measured 4.25s at 128MB, peaking at 109MB) cannot fit inside:
// every invocation times out with no body and no error, presenting as a hang.
func TestFunctionDefaults_ClearAWSImplicitCeilings(t *testing.T) {
	const (
		awsDefaultTimeoutSeconds = 3
		awsDefaultMemoryMB       = 128
	)
	if defaultFunctionTimeoutSeconds <= awsDefaultTimeoutSeconds {
		t.Errorf("defaultFunctionTimeoutSeconds = %d, must exceed AWS's implicit %ds",
			defaultFunctionTimeoutSeconds, awsDefaultTimeoutSeconds)
	}
	if defaultFunctionMemoryMB <= awsDefaultMemoryMB {
		t.Errorf("defaultFunctionMemoryMB = %d, must exceed AWS's implicit %dMB",
			defaultFunctionMemoryMB, awsDefaultMemoryMB)
	}
}

// Sized for an accumulating bundle rather than a single module — see
// nextBundleFunctionMemoryMB for why.
func TestTranslateFunction_NextGetsTheBundleMemoryDefault(t *testing.T) {
	got := translateFunction(&deploymentsv1.ManifestFunction{Framework: frameworkNext})
	if got.MemorySizeMB != nextBundleFunctionMemoryMB {
		t.Errorf("MemorySizeMB = %d, want the Next default %d", got.MemorySizeMB, nextBundleFunctionMemoryMB)
	}
}

// Express/fastify apps are already a single function, so nothing accumulates and
// the flat default still fits.
func TestTranslateFunction_NonNextKeepsTheFlatDefault(t *testing.T) {
	got := translateFunction(&deploymentsv1.ManifestFunction{Framework: "express"})
	if got.MemorySizeMB != defaultFunctionMemoryMB {
		t.Errorf("MemorySizeMB = %d, want default %d", got.MemorySizeMB, defaultFunctionMemoryMB)
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
	got := artifactArchivePath("/proj/.ocel/output", "apps/web/functions/api.func")
	want := "/proj/.ocel/output/apps/web/functions/api.func"
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
	// makes, which is UpdateItem to merge and nothing else. The two live in
	// different languages with nothing linking them, so a missing action is only
	// discovered as a runtime 403 out of the user's revalidateTag call — which is
	// exactly what happened when writeTags moved from PutItem to UpdateItem.
	wantActions := []string{"dynamodb:UpdateItem"}
	if !slices.Equal(ddbStmt.Action, wantActions) {
		t.Errorf("DynamoDB Action = %v, want exactly %v", ddbStmt.Action, wantActions)
	}
	// Exact LeadingKeys matching cannot express a prefix, so the scoping rests
	// on StringLike; a plain StringEquals here would silently grant the table.
	keys := ddbStmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
	if len(keys) != 1 || keys[0] != "TAG#prod#proj123#marketing#build456#*" {
		t.Errorf("LeadingKeys = %v, want the app's own tag partitions", keys)
	}

	// Both tiers read their whole tag state from the snapshot object under the S3
	// grant above, so nothing on this function reads the table any more and no
	// statement may grant it: a read the runtime never issues is a standing read
	// of every tag partition this app's namespace admits.
	for _, stmt := range doc.Statement {
		if strings.Contains(stmt.Resource, "/index/") {
			t.Errorf("policy still grants %v on the index %q", stmt.Action, stmt.Resource)
		}
		for _, read := range []string{"dynamodb:Query", "dynamodb:BatchGetItem", "dynamodb:GetItem"} {
			if slices.Contains(stmt.Action, read) {
				t.Errorf("policy still grants %s on %q", read, stmt.Resource)
			}
		}
	}
}

// TestISRPolicy_CannotReachAnotherAppsPrefix proves two apps deployed side by
// side are sealed off from each other: neither app's role grants any resource
// under the other's prefix, in S3 or in the state table. Both apps share the
// account-global bucket and table, so this scoping is the only thing standing
// between one app's Lambdas and another's cached pages.
func TestISRPolicy_CannotReachAnotherAppsPrefix(t *testing.T) {
	const tableARN = "arn:aws:dynamodb:us-east-1:1234:table/state-abc"
	web := isrConfig{Bucket: "assets-xyz", Prefix: "prod/proj/web/WEB1", Table: "state-abc", TableARN: tableARN}
	admin := isrConfig{Bucket: "assets-xyz", Prefix: "prod/proj/admin/ADM1", Table: "state-abc", TableARN: tableARN}

	webDoc, adminDoc := parsePolicy(t, web), parsePolicy(t, admin)

	if want := "arn:aws:s3:::assets-xyz/prod/proj/web/WEB1/*"; webDoc.Statement[0].Resource != want {
		t.Errorf("web S3 Resource = %q, want %q", webDoc.Statement[0].Resource, want)
	}
	if want := "arn:aws:s3:::assets-xyz/prod/proj/admin/ADM1/*"; adminDoc.Statement[0].Resource != want {
		t.Errorf("admin S3 Resource = %q, want %q", adminDoc.Statement[0].Resource, want)
	}
	if strings.Contains(webDoc.Statement[0].Resource, "admin") {
		t.Errorf("web's S3 grant %q reaches the admin app", webDoc.Statement[0].Resource)
	}

	// The table is addressed by a bare ARN both apps share, so the separation
	// rests entirely on the leading-key condition.
	for _, stmt := range webDoc.Statement[1:] {
		keys := stmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
		if len(keys) != 1 || keys[0] != "TAG#prod#proj#web#WEB1#*" {
			t.Fatalf("web LeadingKeys = %v, want only its own tag partitions", keys)
		}
		if strings.HasPrefix(admin.tagNamespace(), strings.TrimSuffix(keys[0], "*")) {
			t.Errorf("web's LeadingKeys %q admits the admin app's namespace %q", keys[0], admin.tagNamespace())
		}
	}
}

type policyDoc struct {
	Statement []struct {
		Effect    string   `json:"Effect"`
		Action    []string `json:"Action"`
		Resource  string   `json:"Resource"`
		Condition map[string]map[string][]string
	}
}

func parsePolicy(t *testing.T, cfg isrConfig) policyDoc {
	t.Helper()
	raw, err := isrPolicy(cfg)
	if err != nil {
		t.Fatalf("isrPolicy: %v", err)
	}
	var doc policyDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 2 {
		t.Fatalf("got %d statements, want 2", len(doc.Statement))
	}
	return doc
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
	// The index name went with the query that used it: the tag clock reads the
	// snapshot object under OCEL_ISR_PREFIX, and nothing in the bundle reads
	// this variable any more.
	if _, ok := env["OCEL_STATE_TABLE_INDEX"]; ok {
		t.Errorf("OCEL_STATE_TABLE_INDEX = %q, want it unset", env["OCEL_STATE_TABLE_INDEX"])
	}
}

// TestTagNamespace_MatchesTheEdgeContract pins the namespace this deploy grants
// to the one the edge derives for itself. The Lambda tier is handed the finished
// string in its env, so the only other spelling is TypeScript's tagNamespace() —
// and since neither side calls the other, the fixture is what fails when one of
// them moves. The edge reader's own test asserts against the same file.
func TestTagNamespace_MatchesTheEdgeContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "packages", "next-cache", "fixtures", "edge-contract.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var contract struct {
		TagNamespace struct {
			ISRPrefix          string `json:"isrPrefix"`
			PartitionKeyPrefix string `json:"partitionKeyPrefix"`
		} `json:"tagNamespace"`
	}
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	cfg := isrConfig{Prefix: contract.TagNamespace.ISRPrefix}
	if got := cfg.tagNamespace(); got != contract.TagNamespace.PartitionKeyPrefix {
		t.Errorf("tagNamespace() = %q, want %q", got, contract.TagNamespace.PartitionKeyPrefix)
	}
}

// The bucket name is the whole of what a deployed function is told about the
// adopted store: it is what makes the cache handler read and write its entries
// through the ISR writer worker rather than the provider's own bucket. It rides
// in as a plain env var — there is nothing secret left in it, and an SSM
// SecureString would only put a GetParameter on every cold start.
//
// The name is declared independently here and in the handler that reads it, and
// no build step compares them, so the checked-in edge contract is what fails
// when one of them moves. The reader's own test asserts against the same file.
func TestISRCacheStore_NamesTheAdoptedBucketFromTheEdgeContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "packages", "next-cache", "fixtures", "edge-contract.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var contract struct {
		CacheStoreEnv struct {
			Bucket string `json:"bucket"`
		} `json:"cacheStoreEnv"`
	}
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	cfg := isrConfig{
		Bucket:           "assets-xyz",
		Prefix:           "prod/proj123/marketing/build456",
		Table:            "state-abc",
		TableARN:         "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
		CacheStoreBucket: "ocel-edge-cache",
	}

	if got := cfg.env()[contract.CacheStoreEnv.Bucket]; got != "ocel-edge-cache" {
		t.Errorf("%s = %q, want the adopted bucket", contract.CacheStoreEnv.Bucket, got)
	}
}

// The credential the function used to be handed is gone with the publisher that
// read it: no parameter to fetch, no grant to read it, and no decrypt. An R2
// token scopes to a bucket and nothing finer, so one left on a deployed function
// would write every project's cache on the substrate.
func TestISRCacheStore_LeavesNoStandingCredentialOnTheFunction(t *testing.T) {
	cfg := isrConfig{
		Bucket:           "assets-xyz",
		Prefix:           "prod/proj123/marketing/build456",
		Table:            "state-abc",
		TableARN:         "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
		CacheStoreBucket: "ocel-edge-cache",
	}

	env := functionEnv(map[string]string{}, functionArgs{Handler: "index.mjs"}, &cfg, nil)
	for name, value := range env {
		if strings.Contains(name, "ACCESS_KEY") || strings.Contains(name, "SECRET_ACCESS") {
			t.Errorf("env carries %s = %q", name, value)
		}
	}
	if _, ok := env["OCEL_CACHE_STORE_PARAM"]; ok {
		t.Error("OCEL_CACHE_STORE_PARAM is set; the parameter it named carried the R2 keys")
	}

	raw, err := isrPolicy(cfg)
	if err != nil {
		t.Fatalf("isrPolicy: %v", err)
	}
	for _, action := range []string{"ssm:GetParameter", "kms:Decrypt"} {
		if strings.Contains(raw, action) {
			t.Errorf("policy still grants %s; nothing on this role reads a parameter now", action)
		}
	}
	if doc := parsePolicy(t, cfg); len(doc.Statement) != 2 {
		t.Errorf("got %d statements, want the two cache grants and nothing more", len(doc.Statement))
	}
}
