package deploy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
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
	if len(doc.Statement) != 3 {
		t.Fatalf("got %d statements, want 3", len(doc.Statement))
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

	// An index is not covered by its table's ARN, so the tag-sync query needs
	// its own statement against the index ARN — a grant on the table alone 403s
	// the query at runtime. The leading-key constraint is evaluated against the
	// index's partition key here, which is the tag namespace verbatim; the
	// trailing * matches zero characters, so the same wildcard admits it.
	idxStmt := doc.Statement[2]
	if want := cfg.TableARN + "/index/" + bootstrap.StateTableIndexName; idxStmt.Resource != want {
		t.Errorf("index Resource = %q, want %q", idxStmt.Resource, want)
	}
	if want := []string{"dynamodb:Query"}; !slices.Equal(idxStmt.Action, want) {
		t.Errorf("index Action = %v, want exactly %v", idxStmt.Action, want)
	}
	idxKeys := idxStmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
	if len(idxKeys) != 1 || idxKeys[0] != "TAG#prod#proj123#marketing#build456#*" {
		t.Errorf("index LeadingKeys = %v, want the app's own tag partitions", idxKeys)
	}
	if !strings.HasPrefix(cfg.tagNamespace(), strings.TrimSuffix(idxKeys[0], "*")) {
		t.Errorf("tagNamespace %q is not admitted by LeadingKeys %q; the index query would 403", cfg.tagNamespace(), idxKeys[0])
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

	// The table and its index are addressed by a bare ARN both apps share, so
	// the separation rests entirely on the leading-key condition.
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
	if len(doc.Statement) != 3 {
		t.Fatalf("got %d statements, want 3", len(doc.Statement))
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
	// Carried in the environment so the handler never hardcodes an index name
	// the template alone controls.
	if env["OCEL_STATE_TABLE_INDEX"] != bootstrap.StateTableIndexName {
		t.Errorf("OCEL_STATE_TABLE_INDEX = %q, want %q", env["OCEL_STATE_TABLE_INDEX"], bootstrap.StateTableIndexName)
	}
}

// The membrane fails the init when it cannot read the cache-store parameter, so
// a function whose env names one and whose role does not grant it would not
// start at all. The two are asserted together for that reason.
func TestISRCacheStore_GrantsAndNamesTheSameParameter(t *testing.T) {
	const paramARN = "arn:aws:ssm:us-east-1:1234:parameter/ocel/edge/cache-store"
	cfg := isrConfig{
		Bucket:             "assets-xyz",
		Prefix:             "prod/proj123/marketing/build456",
		Table:              "state-abc",
		TableARN:           "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
		CacheStoreParam:    "/ocel/edge/cache-store",
		CacheStoreParamARN: paramARN,
	}

	if got := cfg.env()["OCEL_CACHE_STORE_PARAM"]; got != cfg.CacheStoreParam {
		t.Errorf("OCEL_CACHE_STORE_PARAM = %q, want %q", got, cfg.CacheStoreParam)
	}

	raw, err := isrPolicy(cfg)
	if err != nil {
		t.Fatalf("isrPolicy: %v", err)
	}
	var doc struct {
		Statement []struct {
			Action    []string `json:"Action"`
			Resource  string   `json:"Resource"`
			Condition map[string]map[string]any
		}
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Statement) != 5 {
		t.Fatalf("got %d statements, want 5 (the three cache grants plus ssm and kms)", len(doc.Statement))
	}

	ssmStmt := doc.Statement[3]
	if want := []string{"ssm:GetParameter"}; !slices.Equal(ssmStmt.Action, want) {
		t.Errorf("ssm Action = %v, want exactly %v", ssmStmt.Action, want)
	}
	// Scoped to the one parameter: a wildcard here would hand every function in
	// the account read access to every other parameter, including the edge
	// reader's access key.
	if ssmStmt.Resource != paramARN {
		t.Errorf("ssm Resource = %q, want %q", ssmStmt.Resource, paramARN)
	}

	// The parameter is a SecureString, so GetParameter with decryption also needs
	// kms:Decrypt. The key is the account's default SSM key, whose ARN the deploy
	// cannot know, so scoping rests entirely on the encryption context SSM sets —
	// without the condition this statement would be a decrypt grant on the whole
	// account.
	kmsStmt := doc.Statement[4]
	if want := []string{"kms:Decrypt"}; !slices.Equal(kmsStmt.Action, want) {
		t.Errorf("kms Action = %v, want exactly %v", kmsStmt.Action, want)
	}
	if got := kmsStmt.Condition["StringEquals"]["kms:EncryptionContext:PARAMETER_ARN"]; got != paramARN {
		t.Errorf("kms encryption-context condition = %q, want it bound to %q", got, paramARN)
	}
}

// A substrate with no cache-store parameter must deploy exactly as before: no
// parameter named in the env, and no grant widening the role.
func TestISRCacheStore_AbsentParameterLeavesEnvAndPolicyUntouched(t *testing.T) {
	cfg := isrConfig{
		Bucket:   "assets-xyz",
		Prefix:   "prod/proj123/marketing/build456",
		Table:    "state-abc",
		TableARN: "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
	}

	if _, ok := cfg.env()["OCEL_CACHE_STORE_PARAM"]; ok {
		t.Error("OCEL_CACHE_STORE_PARAM is set; an unset name is what makes the membrane skip the fetch")
	}
	if doc := parsePolicy(t, cfg); len(doc.Statement) != 3 {
		t.Errorf("got %d statements, want the original 3", len(doc.Statement))
	}
}
