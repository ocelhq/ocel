package deploy

import (
	"encoding/json"
	"reflect"
	"testing"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestTranslateFunction_FixedLambdaDefaults(t *testing.T) {
	got := translateFunction(&providerv1.ManifestFunction{
		Runtime: "nodejs20.x",
		Handler: "index.handler",
	})
	if got.Runtime != "nodejs20.x" {
		t.Errorf("Runtime = %q, want nodejs20.x", got.Runtime)
	}
	if got.Handler != "index.handler" {
		t.Errorf("Handler = %q, want index.handler", got.Handler)
	}
}

func TestTranslateFunction_EmptyFallsBackToPinnedDefaults(t *testing.T) {
	got := translateFunction(&providerv1.ManifestFunction{})
	if got.Runtime != defaultFunctionRuntime {
		t.Errorf("Runtime = %q, want default %q", got.Runtime, defaultFunctionRuntime)
	}
	if got.Handler != defaultFunctionHandler {
		t.Errorf("Handler = %q, want default %q", got.Handler, defaultFunctionHandler)
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

// The env injected onto every function mirrors `ocel dev`: one
// OCEL_RESOURCE_<TYPE>_<id> key per manifest resource, keyed by the resource's
// user id (resource.Name), not its logical_name.
func TestResourceEnvKeys_FullSetKeyedByUserID(t *testing.T) {
	manifest := &providerv1.Manifest{
		Resources: []*providerv1.ManifestResource{
			{
				LogicalName: "postgres_main",
				Resource:    &resourcesv1.ResourceIdentifier{Name: "main", Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES},
				Config:      &providerv1.ManifestResource_Postgres{Postgres: &resourcesv1.PostgresConfig{}},
			},
			{
				LogicalName: "bucket_uploads",
				Resource:    &resourcesv1.ResourceIdentifier{Name: "uploads", Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET},
				Config:      &providerv1.ManifestResource_Bucket{Bucket: &resourcesv1.BucketConfig{}},
			},
		},
	}
	got := resourceEnvKeys(manifest)
	want := []string{"OCEL_RESOURCE_POSTGRES_main", "OCEL_RESOURCE_BUCKET_uploads"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resourceEnvKeys() = %v, want %v", got, want)
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
		t.Errorf("address = %q, want the RuntimeService endpoint", parsed.Address)
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
