package deploy

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func TestTranslateFunctionSpec(t *testing.T) {
	t.Run("passes runtime and entrypoint", func(t *testing.T) {
		t.Parallel()
		got := translateFunctionSpec("", providerkit.FunctionSpec{
			Runtime: "nodejs24.x",
			Handler: "src/server.js",
		})
		if got.Runtime != "nodejs24.x" {
			t.Errorf("Runtime = %q, want nodejs24.x", got.Runtime)
		}
		if got.Handler != "src/server.js" {
			t.Errorf("Handler = %q, want src/server.js", got.Handler)
		}
	})

	t.Run("empty falls back to pinned defaults", func(t *testing.T) {
		t.Parallel()
		got := translateFunctionSpec("", providerkit.FunctionSpec{})
		if got.Runtime != defaultFunctionRuntime {
			t.Errorf("Runtime = %q, want default %q", got.Runtime, defaultFunctionRuntime)
		}
		if got.Handler != defaultFunctionEntry {
			t.Errorf("Handler = %q, want default %q", got.Handler, defaultFunctionEntry)
		}
	})

	t.Run("defaults size the function for SSR", func(t *testing.T) {
		t.Parallel()
		got := translateFunctionSpec("", providerkit.FunctionSpec{})
		if got.MemorySizeMB != defaultFunctionMemoryMB {
			t.Errorf("MemorySizeMB = %d, want default %d", got.MemorySizeMB, defaultFunctionMemoryMB)
		}
		if got.TimeoutSeconds != defaultFunctionTimeoutSeconds {
			t.Errorf("TimeoutSeconds = %d, want default %d", got.TimeoutSeconds, defaultFunctionTimeoutSeconds)
		}
	})

	t.Run("Next gets the bundle memory default", func(t *testing.T) {
		t.Parallel()
		got := translateFunctionSpec(frameworkNext, providerkit.FunctionSpec{})
		if got.MemorySizeMB != nextBundleFunctionMemoryMB {
			t.Errorf("MemorySizeMB = %d, want the Next default %d", got.MemorySizeMB, nextBundleFunctionMemoryMB)
		}
	})

	t.Run("non-Next keeps the flat default", func(t *testing.T) {
		t.Parallel()
		got := translateFunctionSpec("express", providerkit.FunctionSpec{})
		if got.MemorySizeMB != defaultFunctionMemoryMB {
			t.Errorf("MemorySizeMB = %d, want default %d", got.MemorySizeMB, defaultFunctionMemoryMB)
		}
	})

	t.Run("what the spec asks for wins over both defaults", func(t *testing.T) {
		t.Parallel()
		got := translateFunctionSpec(frameworkNext, providerkit.FunctionSpec{Memory: 3008, Timeout: 45 * time.Second})
		if got.MemorySizeMB != 3008 {
			t.Errorf("MemorySizeMB = %d, want the spec's own 3008", got.MemorySizeMB)
		}
		if got.TimeoutSeconds != 45 {
			t.Errorf("TimeoutSeconds = %d, want the spec's own 45", got.TimeoutSeconds)
		}
	})
}

func argsFor(functions []*contractv1.ManifestFunction) func(appFunction) functionArgs {
	args := make(map[string]functionArgs, len(functions))
	for _, fn := range functions {
		args[fn.GetLogicalName()] = translateFunctionSpec(fn.GetFramework(), providerkit.FunctionSpec{
			Name:    fn.GetLogicalName(),
			Runtime: fn.GetRuntime(),
			Handler: fn.GetHandler(),
		})
	}
	return func(fn appFunction) functionArgs { return args[fn.Logical] }
}

func TestFunctionDefaults(t *testing.T) {
	t.Run("clear AWS's implicit ceilings", func(t *testing.T) {
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
	})
}

func testMembraneLayerPayload() payloads.Placement {
	return payloads.Placement{
		Bucket: "ocel-artifacts",
		Key:    payloads.Key(membraneLayerKeyPrefix, "beef"),
		SHA256: "beef",
	}
}

func TestMembraneLayer(t *testing.T) {
	t.Parallel()

	code := testMembraneLayerPayload()
	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		stack := testStack(t, "prod", "api")
		layer, err := newMembraneLayer(pctx, membraneLayerCoordinate("shop", stack), code)
		if err != nil {
			return err
		}
		role, err := newFunctionRole(pctx, roleCoordinate("shop", stack), executionRole{App: "api"})
		if err != nil {
			return err
		}
		_, err = registerFunction(pctx, "fn--api--users", functionCoordinate("shop", stack, "fn--api--users"),
			"/users", translateFunctionSpec("", providerkit.FunctionSpec{}), artifactRef{Bucket: "artifacts", Key: "fn.zip"},
			nil, nil, nil, nil, role.Arn, layer.Arn, functionURLAuthIAM)
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	inputs := rec.inputs("aws:lambda/layerVersion:LayerVersion", "layer-membrane")
	if len(inputs) == 0 {
		t.Fatal("no layer version was registered")
	}
	for key, want := range map[string]string{
		"s3Bucket":       code.Bucket,
		"s3Key":          code.Key,
		"sourceCodeHash": code.SHA256,
		"layerName":      "shop-prod-api-membrane-r3f8a1c90",
	} {
		got, ok := inputs[resource.PropertyKey(key)]
		if !ok || !got.IsString() || got.StringValue() != want {
			t.Errorf("%s on the layer = %v, want %q", key, got, want)
		}
	}
	if got := stringsAt(inputs, "compatibleRuntimes"); !slices.Equal(got, []string{membraneLayerRuntime}) {
		t.Errorf("compatibleRuntimes = %v, want %v", got, []string{membraneLayerRuntime})
	}
	if got := stringsAt(inputs, "compatibleArchitectures"); !slices.Equal(got, []string{membraneLayerArchitecture}) {
		t.Errorf("compatibleArchitectures = %v, want %v", got, []string{membraneLayerArchitecture})
	}

	layers := stringsAt(rec.inputs("aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90"), "layers")
	if len(layers) != 1 || !strings.Contains(layers[0], "layer-membrane") {
		t.Errorf("the function's layers = %v, want the stack's own layer version", layers)
	}
}

func TestBytecodeCacheEnabled(t *testing.T) {
	t.Run("off with no override", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "")
		if bytecodeCacheEnabled() {
			t.Error("bytecodeCacheEnabled() = true with no override, want false")
		}
	})

	t.Run("on with OCEL_BYTECODE_CACHE=1", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
		if !bytecodeCacheEnabled() {
			t.Error("bytecodeCacheEnabled() = false with OCEL_BYTECODE_CACHE=1, want true")
		}
	})

	for _, v := range []string{"true", "on", "yes", "TRUE"} {
		t.Run("only \"1\" enables, not "+v, func(t *testing.T) {
			t.Setenv(bytecodeCacheEnv, v)
			if bytecodeCacheEnabled() {
				t.Errorf("bytecodeCacheEnabled() = true with OCEL_BYTECODE_CACHE=%q, want false (only \"1\" enables)", v)
			}
		})
	}
}

func isrCacheFor(t *testing.T, env, project, app, buildID string) isrConfig {
	t.Helper()
	coord := storageCoordinate(env, project, app, releaseOf(deployedAs(buildID)))
	return isrConfig{
		Coord:    coord,
		Bucket:   "assets-xyz",
		Prefix:   isrPrefixOf(coord),
		Table:    "state-abc",
		TableARN: "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
	}
}

func TestISREnv(t *testing.T) {
	t.Run("says nothing about the bytecode cache", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")
		env := cfg.env()
		for _, key := range []string{"OCEL_BYTECODE_PREFIX", "OCEL_BYTECODE_BUCKET"} {
			if _, ok := env[key]; ok {
				t.Errorf("%s = %q, want the bytecode cache carried on its own config", key, env[key])
			}
		}
	})

	t.Run("agrees with the policy scope", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")

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
		if want := "PROJECT#proj123#STACK#prod--marketing--" + releaseTokenFor("build456") + "#TAG#"; env["OCEL_ISR_TAG_NAMESPACE"] != want {
			t.Errorf("OCEL_ISR_TAG_NAMESPACE = %q, want %q", env["OCEL_ISR_TAG_NAMESPACE"], want)
		}
		if _, ok := env["OCEL_STATE_TABLE_INDEX"]; ok {
			t.Errorf("OCEL_STATE_TABLE_INDEX = %q, want it unset", env["OCEL_STATE_TABLE_INDEX"])
		}
	})
}

func bytecodeCacheFor(t *testing.T, env, project, app, buildID string) bytecodeConfig {
	t.Helper()
	coord := storageCoordinate(env, project, app, releaseOf(deployedAs(buildID)))
	return bytecodeConfig{
		Bucket: "assets-xyz",
		Prefix: bytecodePrefixOf(coord),
	}
}

func TestBytecodeEnv(t *testing.T) {
	t.Run("names its own bucket and prefix", func(t *testing.T) {
		cfg := bytecodeCacheFor(t, "prod", "proj123", "marketing", "build456")

		env := cfg.env()

		if want := "prod/proj123/marketing/" + releaseTokenFor("build456") + "/bytecode"; env["OCEL_BYTECODE_PREFIX"] != want {
			t.Errorf("OCEL_BYTECODE_PREFIX = %q, want %q", env["OCEL_BYTECODE_PREFIX"], want)
		}
		if env["OCEL_BYTECODE_BUCKET"] != "assets-xyz" {
			t.Errorf("OCEL_BYTECODE_BUCKET = %q, want the asset bucket", env["OCEL_BYTECODE_BUCKET"])
		}
		if _, ok := env["OCEL_ISR_BUCKET"]; ok {
			t.Error("OCEL_ISR_BUCKET is set; the compile cache has nothing to do with ISR")
		}
	})
}

func TestBytecodePolicy(t *testing.T) {
	t.Run("covers the composed bytecode key and nothing else", func(t *testing.T) {
		cfg := bytecodeCacheFor(t, "prod", "proj123", "marketing", "build456")
		key := fmt.Sprintf("%s/my-function/node22-x64.tar.gz", cfg.Prefix)

		raw, err := bytecodePolicy(cfg)
		if err != nil {
			t.Fatalf("bytecodePolicy: %v", err)
		}
		var doc policyDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("policy is not valid JSON: %v", err)
		}
		if len(doc.Statement) != 1 {
			t.Fatalf("got %d statements, want the one S3 grant", len(doc.Statement))
		}

		stmt := doc.Statement[0]
		if want := "arn:aws:s3:::assets-xyz/prod/proj123/marketing/" + releaseTokenFor("build456") + "/bytecode/*"; stmt.Resource != want {
			t.Errorf("Resource = %q, want %q", stmt.Resource, want)
		}
		if want := []string{"s3:GetObject", "s3:PutObject"}; !slices.Equal(stmt.Action, want) {
			t.Errorf("Action = %v, want exactly %v", stmt.Action, want)
		}
		dir := strings.TrimSuffix(strings.TrimPrefix(stmt.Resource, "arn:aws:s3:::"), "*")
		if !strings.HasPrefix(cfg.Bucket+"/"+key, dir) {
			t.Errorf("bytecode key %s/%s is not covered by %q", cfg.Bucket, key, stmt.Resource)
		}
		if strings.Contains(raw, "dynamodb") {
			t.Errorf("policy mentions DynamoDB; the compile cache keeps no tag ledger: %s", raw)
		}
	})

	t.Run("cannot reach another app's prefix", func(t *testing.T) {
		web := bytecodeCacheFor(t, "prod", "proj", "web", "WEB1")
		admin := bytecodeCacheFor(t, "prod", "proj", "admin", "ADM1")

		raw, err := bytecodePolicy(web)
		if err != nil {
			t.Fatalf("bytecodePolicy: %v", err)
		}
		if strings.Contains(raw, admin.Prefix) {
			t.Errorf("web's policy reaches admin's prefix %q: %s", admin.Prefix, raw)
		}
	})
}

func TestISRPolicy(t *testing.T) {
	t.Run("scopes to the app's own namespace", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")

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
		if want := "arn:aws:s3:::assets-xyz/prod/proj123/marketing/" + releaseTokenFor("build456") + "/isr/*"; s3Stmt.Resource != want {
			t.Errorf("S3 Resource = %q, want %q", s3Stmt.Resource, want)
		}

		ddbStmt := doc.Statement[1]
		if ddbStmt.Resource != cfg.TableARN {
			t.Errorf("DynamoDB Resource = %q, want the table ARN", ddbStmt.Resource)
		}
		wantActions := []string{"dynamodb:UpdateItem"}
		if !slices.Equal(ddbStmt.Action, wantActions) {
			t.Errorf("DynamoDB Action = %v, want exactly %v", ddbStmt.Action, wantActions)
		}
		keys := ddbStmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
		if want := "PROJECT#proj123#STACK#prod--marketing--" + releaseTokenFor("build456") + "#TAG#*"; len(keys) != 1 || keys[0] != want {
			t.Errorf("LeadingKeys = %v, want the app's own tag partitions", keys)
		}

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
	})

	t.Run("cannot reach another app's prefix", func(t *testing.T) {
		web := isrCacheFor(t, "prod", "proj", "web", "WEB1")
		admin := isrCacheFor(t, "prod", "proj", "admin", "ADM1")
		preview := isrCacheFor(t, "pr-7", "proj", "web", "WEB1")
		redeployed := isrCacheFor(t, "prod", "proj", "web", "WEB2")

		webDoc, adminDoc := parsePolicy(t, web), parsePolicy(t, admin)

		if want := "arn:aws:s3:::assets-xyz/" + web.Prefix + "/*"; webDoc.Statement[0].Resource != want {
			t.Errorf("web S3 Resource = %q, want %q", webDoc.Statement[0].Resource, want)
		}
		if want := "arn:aws:s3:::assets-xyz/" + admin.Prefix + "/*"; adminDoc.Statement[0].Resource != want {
			t.Errorf("admin S3 Resource = %q, want %q", adminDoc.Statement[0].Resource, want)
		}
		if strings.Contains(webDoc.Statement[0].Resource, "admin") {
			t.Errorf("web's S3 grant %q reaches the admin app", webDoc.Statement[0].Resource)
		}

		for _, stmt := range webDoc.Statement[1:] {
			keys := stmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
			if want := web.tagNamespace() + "*"; len(keys) != 1 || keys[0] != want {
				t.Fatalf("web LeadingKeys = %v, want only %q", keys, want)
			}
			for _, tag := range []string{"products", "a#b", ""} {
				if key := isrTagKey(web, tag); !admits(t, keys[0], key) {
					t.Errorf("web's LeadingKeys %q denies its own tag key %q", keys[0], key)
				}
			}
			for _, other := range []isrConfig{admin, preview, redeployed} {
				for _, tag := range []string{"products", ""} {
					if key := isrTagKey(other, tag); admits(t, keys[0], key) {
						t.Errorf("web's LeadingKeys %q admits %q, which web must not write", keys[0], key)
					}
				}
			}
			for _, key := range []string{
				naming.ProjectKey("proj"),
				naming.VarsKey("proj", "production"),
				naming.StackKey("proj", web.Coord.Stack()),
			} {
				if admits(t, keys[0], key) {
					t.Errorf("web's LeadingKeys %q admits %q, which is not a tag partition", keys[0], key)
				}
			}
		}
	})
}

func isrTagKey(c isrConfig, tag string) string {
	return c.tagNamespace() + tag
}

func admits(t *testing.T, pattern, key string) bool {
	t.Helper()
	if strings.ContainsAny(pattern+key, "?[]/\\") {
		t.Fatalf("pattern %q or key %q carries a character path.Match reads differently from IAM StringLike", pattern, key)
	}
	ok, err := path.Match(pattern, key)
	if err != nil {
		t.Fatalf("path.Match(%q, %q): %v", pattern, key, err)
	}
	return ok
}

func TestFunctionEnvKey(t *testing.T) {
	cases := []struct {
		name     string
		typ      linksv1.LinkType
		userID   string
		wantName string
	}{
		{"postgres uses the type's env fragment and user ID", linksv1.LinkType_LINK_TYPE_POSTGRES, "main", "OCEL_RESOURCE_POSTGRES_main"},
		{"bucket uses the type's env fragment and user ID", linksv1.LinkType_LINK_TYPE_BUCKET, "uploads", "OCEL_RESOURCE_BUCKET_uploads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := functionEnvKey(tc.typ, tc.userID); got != tc.wantName {
				t.Errorf("functionEnvKey(%v, %s) = %q, want %q", tc.typ, tc.userID, got, tc.wantName)
			}
		})
	}
}

func TestArtifactArchivePath(t *testing.T) {
	t.Run("resolves relative to the output root", func(t *testing.T) {
		got := artifactArchivePath("/proj/.ocel/output", "apps/web/functions/api.func")
		want := "/proj/.ocel/output/apps/web/functions/api.func"
		if got != want {
			t.Errorf("artifactArchivePath() = %q, want %q", got, want)
		}
	})
}

func TestCollectFunctionOutput(t *testing.T) {
	t.Run("reports the URL keyed by logical name", func(t *testing.T) {
		out := collectFunctionOutput("api", "https://abc.lambda-url.us-east-1.on.aws/")
		if out.GetLogicalName() != "api" {
			t.Errorf("LogicalName = %q, want api", out.GetLogicalName())
		}
		if out.GetUrl() != "https://abc.lambda-url.us-east-1.on.aws/" {
			t.Errorf("url = %q, want the Function URL", out.GetUrl())
		}
	})
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

func TestTagNamespace(t *testing.T) {
	t.Run("matches the edge contract", func(t *testing.T) {
		body := nextCacheFixture(t, "edge-contract.json")
		var contract struct {
			TagNamespace struct {
				Coordinate struct {
					Project string `json:"project"`
					Env     string `json:"env"`
					App     string `json:"app"`
					Release string `json:"release"`
				} `json:"coordinate"`
				ISRPrefix          string `json:"isrPrefix"`
				PartitionKeyPrefix string `json:"partitionKeyPrefix"`
			} `json:"tagNamespace"`
		}
		if err := json.Unmarshal(body, &contract); err != nil {
			t.Fatalf("parse fixture: %v", err)
		}

		facts := contract.TagNamespace.Coordinate
		release, err := naming.ParseRelease(facts.Release)
		if err != nil {
			t.Fatalf("ParseRelease(%q): %v", facts.Release, err)
		}
		coord := storageCoordinate(facts.Env, facts.Project, facts.App, release)

		if got := isrPrefixOf(coord); got != contract.TagNamespace.ISRPrefix {
			t.Errorf("isrPrefixOf() = %q, want the contract's isrPrefix %q", got, contract.TagNamespace.ISRPrefix)
		}
		cfg := isrConfig{Coord: coord}
		if got := cfg.tagNamespace(); got != contract.TagNamespace.PartitionKeyPrefix {
			t.Errorf("tagNamespace() = %q, want %q", got, contract.TagNamespace.PartitionKeyPrefix)
		}
	})

	t.Run("refuses a cache with no coordinate", func(t *testing.T) {
		if got := (isrConfig{Prefix: "prod/proj/web/r3f8a1c9d/isr"}).tagNamespace(); got != "" {
			t.Errorf("tagNamespace() = %q, want %q — a rendered path is not a coordinate", got, "")
		}
		if _, err := isrPolicy(isrConfig{Prefix: "prod/proj/web/r3f8a1c9d/isr"}); err == nil {
			t.Error("isrPolicy accepted a cache with no coordinate; an unscoped tag grant must never render")
		}
	})
}

func TestISRCacheStore(t *testing.T) {
	t.Run("names the adopted bucket from the edge contract", func(t *testing.T) {
		body := nextCacheFixture(t, "edge-contract.json")
		var contract struct {
			CacheStoreEnv struct {
				Bucket string `json:"bucket"`
			} `json:"cacheStoreEnv"`
		}
		if err := json.Unmarshal(body, &contract); err != nil {
			t.Fatalf("parse fixture: %v", err)
		}

		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")
		cfg.CacheStoreBucket = "ocel-edge-cache"

		if got := cfg.env()[contract.CacheStoreEnv.Bucket]; got != "ocel-edge-cache" {
			t.Errorf("%s = %q, want the adopted bucket", contract.CacheStoreEnv.Bucket, got)
		}
	})

	t.Run("leaves no standing credential on the function", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")
		cfg.CacheStoreBucket = "ocel-edge-cache"

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
	})
}
