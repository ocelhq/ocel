package deploy

import (
	"encoding/json"
	"testing"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestTranslateFunction_PassesRuntimeAndEntrypoint(t *testing.T) {
	got := translateFunction(&providerv1.ManifestFunction{
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
	got := translateFunction(&providerv1.ManifestFunction{})
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
