package function

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

func TestAnARM64FunctionTakesTheARM64RuntimeLayerAndNamesItsArchitecture(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		stack := testStack(t, "prod", "api")
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{App: "api", Boundary: componenttest.BoundaryARN})
		if err != nil {
			return err
		}
		args, err := NewArgs(buildoutput.FrameworkGo, provider.FunctionSpec{
			Framework: buildoutput.Framework{Name: buildoutput.FrameworkGo, Arch: arch.ARM64},
			EntryFile: "web",
		})
		if err != nil {
			return err
		}
		stackFunctions := AppStack{
			Project:   "shop",
			Stack:     stack,
			Functions: []AppFunction{{Logical: "fn--api--users", RouteID: "/users"}},
			Args:      func(AppFunction) Args { return args },
			Layers: map[string]string{
				arch.ARM64: testRuntimeLayerARN(arch.ARM64),
			},
			Artifacts: map[string]Artifact{"fn--api--users": {Bucket: "artifacts", Key: "fn.zip"}},
			RoleArn:   role.Arn,
		}
		return stackFunctions.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	inputs := rec.inputs("aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90")
	if got := stringsAt(inputs, "architectures"); !slices.Equal(got, []string{arch.ARM64}) {
		t.Errorf("architectures = %v, want [%s] — the runtime and the app's own binary are built for one machine", got, arch.ARM64)
	}
	if got, want := stringsAt(inputs, "layers"), testRuntimeLayerARN(arch.ARM64); len(got) != 1 || got[0] != want {
		t.Errorf("the function's layers = %v, want [%s]: the arm64 runtime the account's bootstrap published", got, want)
	}
	if got, ok := inputs[resource.PropertyKey("runtime")]; !ok || got.StringValue() != providedRuntime {
		t.Errorf("runtime = %v, want %q: an app with its own binary needs no managed language runtime", got, providedRuntime)
	}
	if got, ok := inputs[resource.PropertyKey("handler")]; !ok || got.StringValue() != providedHandler {
		t.Errorf("handler = %v, want %q: %s boots the executable of that name", got, providedHandler, providedRuntime)
	}
}

func TestADryPlanBootsAFunctionThroughTheRuntimeLayerTheDeployWouldPublish(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		stack := testStack(t, "prod", "api")
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{App: "api", Boundary: componenttest.BoundaryARN})
		if err != nil {
			return err
		}
		args, err := NewArgs(buildoutput.FrameworkGo, provider.FunctionSpec{
			Framework: buildoutput.Framework{Name: buildoutput.FrameworkGo, Arch: arch.X8664},
			EntryFile: "web",
		})
		if err != nil {
			return err
		}
		stackFunctions := AppStack{
			Project:   "shop",
			Stack:     stack,
			Functions: []AppFunction{{Logical: "fn--api--users", RouteID: "/users"}},
			Args:      func(AppFunction) Args { return args },
			Layers:    map[string]string{arch.X8664: awsports.RuntimeLayerToPublish},
			Artifacts: map[string]Artifact{"fn--api--users": {Bucket: "artifacts", Key: "fn.zip"}},
			RoleArn:   role.Arn,
		}
		return stackFunctions.Declare(pctx)
	}
	dryRun := func(info *pulumi.RunInfo) { info.DryRun = true }
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec), dryRun); err != nil {
		t.Fatalf("run program: %v", err)
	}

	layers, ok := rec.inputs("aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90")[resource.PropertyKey("layers")]
	if !ok || !layers.IsArray() || len(layers.ArrayValue()) != 1 {
		t.Fatalf("the function's layers = %v, want the one runtime layer the deploy would publish", layers)
	}
	if layer := layers.ArrayValue()[0]; layer.IsString() {
		t.Errorf("the function's layer = %q, want it unknown until the deploy publishes it", layer.StringValue())
	}
}

func testRuntimeLayers() map[string]string {
	return map[string]string{
		arch.X8664: testRuntimeLayerARN(arch.X8664),
		arch.ARM64: testRuntimeLayerARN(arch.ARM64),
	}
}

func testRuntimeLayerARN(architecture string) string {
	return "arn:aws:lambda:us-east-1:123456789012:layer:ocel-runtime-" + architecture + "-beefbeefbeef:3"
}

func TestTheReleaseBootsThroughTheAccountsRuntimeAndPublishesNoneOfItsOwn(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		stack := testStack(t, "prod", "api")
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{App: "api", Boundary: componenttest.BoundaryARN})
		if err != nil {
			return err
		}
		args, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			return err
		}
		return AppStack{
			Project:   "shop",
			Stack:     stack,
			Functions: []AppFunction{{Logical: "fn--api--users", RouteID: "/users"}},
			Args:      func(AppFunction) Args { return args },
			Layers:    testRuntimeLayers(),
			Artifacts: map[string]Artifact{"fn--api--users": {Bucket: "artifacts", Key: "fn.zip"}},
			RoleArn:   role.Arn,
		}.Declare(pctx)
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	if published := rec.registered("aws:lambda/layerVersion:LayerVersion"); len(published) != 0 {
		t.Errorf("the release stack publishes %v, want the layers the account's bootstrap already published", published)
	}
	layers := stringsAt(rec.inputs("aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90"), "layers")
	if want := testRuntimeLayerARN(arch.X8664); len(layers) != 1 || layers[0] != want {
		t.Errorf("the function's layers = %v, want [%s]: the version ARN the bootstrap published", layers, want)
	}
}

func TestTheBytecodeCacheFollowsTheSpecWhateverTheOperatorsShell(t *testing.T) {
	for _, shell := range []string{"", "0", "1"} {
		t.Run("OCEL_BYTECODE_CACHE="+shell, func(t *testing.T) {
			t.Setenv("OCEL_BYTECODE_CACHE", shell)
			cache := Bytecode{Bucket: "assets-xyz", Prefix: "prod/proj/api/API1/bytecode"}

			env := Env(map[string]string{}, Args{Handler: "index.mjs"}, nil, &cache)
			if env["OCEL_BYTECODE_PREFIX"] != cache.Prefix || env["OCEL_BYTECODE_BUCKET"] != cache.Bucket {
				t.Errorf("env = %v, want the cache the spec names, the same from every shell", env)
			}
			if targets := warmTargets(nextManifest(), map[string]*Bytecode{"web": &cache}, map[string]string{"web_index": "fn"}); len(targets) != 1 {
				t.Errorf("warmTargets = %+v, want the function the spec gives a cache warmed from every shell", targets)
			}
		})
	}
}

func isrCacheFor(t *testing.T, env, project, app, buildID string) site.ISR {
	t.Helper()
	coord := storageCoordinate(env, project, app, deployedAs(buildID).Token())
	return site.ISR{
		Coord:    coord,
		Bucket:   "assets-xyz",
		Table:    "state-abc",
		TableARN: "arn:aws:dynamodb:us-east-1:1234:table/state-abc",
	}
}

func TestISREnv(t *testing.T) {
	t.Run("says nothing about the bytecode cache", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")
		env := cfg.Env()
		for _, key := range []string{"OCEL_BYTECODE_PREFIX", "OCEL_BYTECODE_BUCKET"} {
			if _, ok := env[key]; ok {
				t.Errorf("%s = %q, want the bytecode cache passed on its own config", key, env[key])
			}
		}
	})

	t.Run("agrees with the policy scope", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")

		env := cfg.Env()

		if env["OCEL_ISR_BUCKET"] != "assets-xyz" {
			t.Errorf("OCEL_ISR_BUCKET = %q", env["OCEL_ISR_BUCKET"])
		}
		if want := "proj123/prod/marketing/" + releaseTokenFor("build456"); env["OCEL_STORAGE_COORDINATE"] != want {
			t.Errorf("OCEL_STORAGE_COORDINATE = %q, want %q", env["OCEL_STORAGE_COORDINATE"], want)
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

func bytecodeCacheFor(t *testing.T, env, project, app, buildID string) Bytecode {
	t.Helper()
	coord := storageCoordinate(env, project, app, deployedAs(buildID).Token())
	return Bytecode{
		Bucket: "assets-xyz",
		Prefix: bytecodePrefixOf(coord),
	}
}

func TestBytecodeEnv(t *testing.T) {
	t.Run("names its own bucket and prefix", func(t *testing.T) {
		cfg := bytecodeCacheFor(t, "prod", "proj123", "marketing", "build456")

		env := cfg.env()

		if want := "bytecode/proj123/prod/marketing/" + releaseTokenFor("build456"); env["OCEL_BYTECODE_PREFIX"] != want {
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
		if want := []string{"arn:aws:s3:::assets-xyz/bytecode/proj123/prod/marketing/" + releaseTokenFor("build456") + "/*"}; !slices.Equal(stmt.Resource, want) {
			t.Errorf("Resource = %q, want %q", stmt.Resource, want)
		}
		if want := []string{"s3:GetObject", "s3:PutObject"}; !slices.Equal(stmt.Action, want) {
			t.Errorf("Action = %v, want exactly %v", stmt.Action, want)
		}
		dir := strings.TrimSuffix(strings.TrimPrefix(stmt.Resource[0], "arn:aws:s3:::"), "*")
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

func TestISREdgeReadable(t *testing.T) {
	t.Run("tells the cache handler to tag its page entries when the edge reads the asset bucket", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")
		cfg.EdgeReadable = true

		if got := cfg.Env()["OCEL_ISR_EDGE_READABLE"]; got != "1" {
			t.Errorf("OCEL_ISR_EDGE_READABLE = %q, want 1", got)
		}
	})

	t.Run("says nothing about it otherwise", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")

		if got, ok := cfg.Env()["OCEL_ISR_EDGE_READABLE"]; ok {
			t.Errorf("OCEL_ISR_EDGE_READABLE = %q, want it unset", got)
		}
	})

	t.Run("lets the function tag only its page entries, and only when the edge reads", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")
		without, err := cfg.Policy()
		if err != nil {
			t.Fatalf("ISR.Policy: %v", err)
		}
		cfg.EdgeReadable = true
		with, err := cfg.Policy()
		if err != nil {
			t.Fatalf("ISR.Policy: %v", err)
		}

		if strings.Contains(without, "s3:PutObjectTagging") {
			t.Errorf("policy grants tagging with no edge reading: %s", without)
		}
		var doc struct {
			Statement []struct {
				Action   []string `json:"Action"`
				Resource any      `json:"Resource"`
			} `json:"Statement"`
		}
		if err := json.Unmarshal([]byte(with), &doc); err != nil {
			t.Fatalf("parse policy: %v", err)
		}
		var tagged []any
		for _, statement := range doc.Statement {
			if slices.Contains(statement.Action, "s3:PutObjectTagging") {
				tagged = append(tagged, statement.Resource)
			}
		}
		want := fmt.Sprintf("arn:aws:s3:::%s/pages/proj123/prod/marketing/%s/*", cfg.Bucket, releaseTokenFor("build456"))
		if len(tagged) != 1 || tagged[0] != want {
			t.Errorf("s3:PutObjectTagging is granted on %v, want only %s", tagged, want)
		}
	})
}

func TestISRPolicy(t *testing.T) {
	t.Run("scopes to the app's own namespace", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")

		raw, err := cfg.Policy()
		if err != nil {
			t.Fatalf("ISR.Policy: %v", err)
		}

		var doc policyDoc
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("policy is not valid JSON: %v", err)
		}
		if len(doc.Statement) != 2 {
			t.Fatalf("got %d statements, want 2", len(doc.Statement))
		}

		s3Stmt := doc.Statement[0]
		at := "/proj123/prod/marketing/" + releaseTokenFor("build456") + "/*"
		if want := []string{
			"arn:aws:s3:::assets-xyz/pages" + at,
			"arn:aws:s3:::assets-xyz/fetch-cache" + at,
			"arn:aws:s3:::assets-xyz/use-cache" + at,
			"arn:aws:s3:::assets-xyz/tag-clock" + at,
		}; !slices.Equal(s3Stmt.Resource, want) {
			t.Errorf("S3 Resource = %q, want %q", s3Stmt.Resource, want)
		}

		ddbStmt := doc.Statement[1]
		if !slices.Equal(ddbStmt.Resource, []string{cfg.TableARN}) {
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
			if strings.Contains(strings.Join(stmt.Resource, " "), "/index/") {
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

		if want := "arn:aws:s3:::assets-xyz/" + web.Coord.StoragePrefix(naming.ObjectPages) + "*"; !slices.Contains(webDoc.Statement[0].Resource, want) {
			t.Errorf("web S3 Resource = %q, want it to hold %q", webDoc.Statement[0].Resource, want)
		}
		if want := "arn:aws:s3:::assets-xyz/" + admin.Coord.StoragePrefix(naming.ObjectPages) + "*"; !slices.Contains(adminDoc.Statement[0].Resource, want) {
			t.Errorf("admin S3 Resource = %q, want it to hold %q", adminDoc.Statement[0].Resource, want)
		}
		if strings.Contains(strings.Join(webDoc.Statement[0].Resource, " "), "admin") {
			t.Errorf("web's S3 grant %q reaches the admin app", webDoc.Statement[0].Resource)
		}

		for _, stmt := range webDoc.Statement[1:] {
			keys := stmt.Condition["ForAllValues:StringLike"]["dynamodb:LeadingKeys"]
			if want := web.TagNamespace() + "*"; len(keys) != 1 || keys[0] != want {
				t.Fatalf("web LeadingKeys = %v, want only %q", keys, want)
			}
			for _, tag := range []string{"products", "a#b", ""} {
				if key := isrTagKey(web, tag); !admits(t, keys[0], key) {
					t.Errorf("web's LeadingKeys %q denies its own tag key %q", keys[0], key)
				}
			}
			for _, other := range []site.ISR{admin, preview, redeployed} {
				for _, tag := range []string{"products", ""} {
					if key := isrTagKey(other, tag); admits(t, keys[0], key) {
						t.Errorf("web's LeadingKeys %q admits %q, which web must not write", keys[0], key)
					}
				}
			}
			for _, key := range []string{
				"PROJECT#proj",
				"PROJECT#proj#CLASS#production",
				naming.StackKey("proj", web.Coord.Stack()),
			} {
				if admits(t, keys[0], key) {
					t.Errorf("web's LeadingKeys %q admits %q, which is not a tag partition", keys[0], key)
				}
			}
		}
	})
}

func isrTagKey(c site.ISR, tag string) string {
	return c.TagNamespace() + tag
}

func admits(t *testing.T, pattern, key string) bool {
	t.Helper()
	if strings.ContainsAny(pattern+key, "?[]/\\") {
		t.Fatalf("pattern %q or key %q contains a character path.Match reads differently from IAM StringLike", pattern, key)
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
		typ      bindingsv1.BindingType
		userID   string
		wantName string
	}{
		{"postgres uses the type's env fragment and user ID", bindingsv1.BindingType_BINDING_TYPE_POSTGRES, "main", "OCEL_RESOURCE_POSTGRES_main"},
		{"bucket uses the type's env fragment and user ID", bindingsv1.BindingType_BINDING_TYPE_BUCKET, "uploads", "OCEL_RESOURCE_BUCKET_uploads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bindings.ResourceEnvName(tc.typ, tc.userID); got != tc.wantName {
				t.Errorf("bindings.ResourceEnvName(%v, %s) = %q, want %q", tc.typ, tc.userID, got, tc.wantName)
			}
		})
	}
}

func TestArtifactArchivePath(t *testing.T) {
	t.Run("resolves relative to the output root", func(t *testing.T) {
		got := artifactArchivePath("/proj/"+buildoutput.StateDir+"/output", "apps/web/functions/api.func")
		want := "/proj/" + buildoutput.StateDir + "/output/apps/web/functions/api.func"
		if got != want {
			t.Errorf("artifactArchivePath() = %q, want %q", got, want)
		}
	})
}

type policyDoc struct {
	Statement []struct {
		Effect    string    `json:"Effect"`
		Action    []string  `json:"Action"`
		Resource  resources `json:"Resource"`
		Condition map[string]map[string][]string
	}
}

type resources []string

func (r *resources) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*r = resources{one}
		return nil
	}
	return json.Unmarshal(raw, (*[]string)(r))
}

func parsePolicy(t *testing.T, cfg site.ISR) policyDoc {
	t.Helper()
	raw, err := cfg.Policy()
	if err != nil {
		t.Fatalf("ISR.Policy: %v", err)
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
				StorageCoordinate  string `json:"storageCoordinate"`
				PartitionKeyPrefix string `json:"partitionKeyPrefix"`
			} `json:"tagNamespace"`
		}
		if err := json.Unmarshal(body, &contract); err != nil {
			t.Fatalf("parse fixture: %v", err)
		}

		facts := contract.TagNamespace.Coordinate
		release, err := naming.ParseReleaseToken(facts.Release)
		if err != nil {
			t.Fatalf("ParseRelease(%q): %v", facts.Release, err)
		}
		coord := storageCoordinate(facts.Env, facts.Project, facts.App, release)

		if got := coord.StorageCoordinate(); got != contract.TagNamespace.StorageCoordinate {
			t.Errorf("StorageCoordinate() = %q, want the contract's %q", got, contract.TagNamespace.StorageCoordinate)
		}
		cfg := site.ISR{Coord: coord}
		if got := cfg.TagNamespace(); got != contract.TagNamespace.PartitionKeyPrefix {
			t.Errorf("TagNamespace() = %q, want %q", got, contract.TagNamespace.PartitionKeyPrefix)
		}
	})

	t.Run("refuses a cache with no coordinate", func(t *testing.T) {
		if got := (site.ISR{Bucket: "assets-xyz"}).TagNamespace(); got != "" {
			t.Errorf("TagNamespace() = %q, want %q", got, "")
		}
		if _, err := (site.ISR{Bucket: "assets-xyz"}).Policy(); err == nil {
			t.Error("ISR.Policy accepted a cache with no coordinate; an unscoped tag grant must never render")
		}
	})
}

func TestISRCacheStore(t *testing.T) {
	t.Run("leaves no long-lived credential on the function", func(t *testing.T) {
		cfg := isrCacheFor(t, "prod", "proj123", "marketing", "build456")

		env := Env(map[string]string{}, Args{Handler: "index.mjs"}, &cfg, nil)
		for name, value := range env {
			if strings.Contains(name, "ACCESS_KEY") || strings.Contains(name, "SECRET_ACCESS") {
				t.Errorf("env contains %s = %q", name, value)
			}
		}

		raw, err := cfg.Policy()
		if err != nil {
			t.Fatalf("ISR.Policy: %v", err)
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

func TestFunctionLogGroup(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		stack := testStack(t, "prod", "api")
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{App: "api", Boundary: componenttest.BoundaryARN})
		if err != nil {
			return err
		}
		args, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			return err
		}
		_, err = registerFunction(pctx, "fn--api--users", Coordinate("shop", stack, "fn--api--users"),
			"/users", args, Artifact{Bucket: "artifacts", Key: "fn.zip"},
			nil, nil, nil, nil, role.Arn, pulumi.StringArray{pulumi.String(mockAccountARN("lambda", "layer:runtime:1"))}, functionurl.AuthIAM, "")
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	group := rec.inputs("aws:cloudwatch/logGroup:LogGroup", naming.ResourceID(naming.KindFunction, "users", "logs"))
	if len(group) == 0 {
		t.Fatal("the deploy owns no log group for the function, so Lambda mints one on first invoke that the teardown never reclaims")
	}
	want := component.LambdaLogGroupPrefixFor("shop-prod-api-users-r3f8a1c90")
	if got := group["namePrefix"]; !got.IsString() || got.StringValue() != want {
		t.Errorf("the log group's namePrefix = %v, want %q", got, want)
	}
	if got, ok := group["name"]; ok {
		t.Errorf("the log group fixes its name to %v, so two stacks sharing a base collide on CreateLogGroup", got)
	}
	retention, ok := group["retentionInDays"]
	if !ok || !retention.IsNumber() || retention.NumberValue() <= 0 {
		t.Errorf("the log group's retentionInDays = %v, want a finite retention rather than events kept forever", retention)
	}

	logging := rec.object(t, "aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90", "loggingConfig")
	if got := logging["logGroup"]; !got.IsString() || !strings.HasPrefix(got.StringValue(), want) {
		t.Errorf("the function's loggingConfig.logGroup = %v, want the group the deploy owns under %q", got, want)
	}
}

func TestAFunctionsEnvironmentIsSealedUnderTheTierVariablesKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		key  string
	}{
		{"an account with a variables key seals every function's environment under it", productionVariablesKeyARN},
		{"an account without one leaves Lambda's own key in place", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &inputRecorder{}
			program := func(pctx *pulumi.Context) error {
				stack := testStack(t, "prod", "api")
				role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{App: "api", Boundary: componenttest.BoundaryARN})
				if err != nil {
					return err
				}
				args, err := NewArgs("", provider.FunctionSpec{})
				if err != nil {
					return err
				}
				_, err = registerFunction(pctx, "fn--api--users", Coordinate("shop", stack, "fn--api--users"),
					"/users", args, Artifact{Bucket: "artifacts", Key: "fn.zip"},
					nil, nil, nil, nil, role.Arn, pulumi.StringArray{pulumi.String(mockAccountARN("lambda", "layer:runtime:1"))}, functionurl.AuthIAM, tc.key)
				return err
			}
			if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
				t.Fatalf("run program: %v", err)
			}

			fn := rec.inputs("aws:lambda/function:Function", "shop-prod-api-users-r3f8a1c90")
			got, set := fn["kmsKeyArn"]
			if tc.key == "" {
				if set {
					t.Errorf("kmsKeyArn = %v, want none where the account names no key", got)
				}
				return
			}
			if !set || !got.IsString() || got.StringValue() != tc.key {
				t.Errorf("kmsKeyArn = %v, want %q: the data key in OCEL_VARIABLES_ENVELOPE is otherwise readable by anyone in the account with lambda:GetFunctionConfiguration", got, tc.key)
			}
		})
	}
}

func TestARoleWithNoBoundaryIsRefusedRatherThanMintedUncapped(t *testing.T) {
	t.Parallel()

	program := func(pctx *pulumi.Context) error {
		_, err := NewRole(pctx, component.AppRoleCoordinate("shop", testStack(t, "prod", "api")), ExecutionRole{App: "api"})
		return err
	}
	err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", &inputRecorder{}))
	if err == nil || !strings.Contains(err.Error(), "boundary") {
		t.Fatalf("newFunctionRole with no boundary = %v, want a refusal naming the boundary", err)
	}
}

func TestAFunctionRoleGrantsTheVariablesKeyOnlyWhereTheAccountNamesOne(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		key  string
	}{
		{"an account with a variables key grants decrypt on it", productionVariablesKeyARN},
		{"an account without one declares no variables policy", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &inputRecorder{}
			program := func(pctx *pulumi.Context) error {
				_, err := NewRole(pctx, component.AppRoleCoordinate("shop", testStack(t, "prod", "api")), ExecutionRole{
					App: "api", Boundary: componenttest.BoundaryARN, Variables: variables.Reader{KeyARN: tc.key},
				})
				return err
			}
			if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--api", rec)); err != nil {
				t.Fatalf("run program: %v", err)
			}

			name := naming.ResourceID(naming.KindRole, component.AppRoleLocalName, "policy", "variables", "read")
			raw, declared := rec.inputs(rolePolicyToken, name)["policy"]
			if tc.key == "" {
				if declared {
					t.Errorf("%s = %v, want none: IAM refuses a policy that grants nothing as malformed", name, raw)
				}
				return
			}
			if !declared || !raw.IsString() || !strings.Contains(raw.StringValue(), tc.key) {
				t.Errorf("%s = %v, want a grant on %q", name, raw, tc.key)
			}
		})
	}
}
