package function

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/functionurl"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
)

func TestFunctionCoordinate(t *testing.T) {
	t.Parallel()

	t.Run("the physical name is the app fence, project, env, app, route and release", func(t *testing.T) {
		t.Parallel()

		if got, want := namedFunction(testStack(t, "prod", "web"), "fn--web--api-users").Pulumi, "ocel-app-shop-prod-web-api-users-r3f8a1c90"; got != want {
			t.Fatalf("Name = %q, want %q", got, want)
		}
	})

	t.Run("a route too long to name within Lambda's limit is refused", func(t *testing.T) {
		t.Parallel()

		route := strings.Repeat("very-long-route-segment-", 6)
		if _, err := Name(testNamespace, "shop", testStack(t, "prod", "web"), "fn--web--"+route); err == nil || !strings.Contains(err.Error(), "64") {
			t.Fatalf("Name = %v, want a refusal naming Lambda's 64 characters", err)
		}
	})

	t.Run("its URL and the URL's permissions are named under it", func(t *testing.T) {
		t.Parallel()

		names := mustResourceNames(t, testStack(t, "prod", "web"), "fn--web--index")
		if got, want := names["url"][0].Name, "ocel-app-shop-prod-web-index-r3f8a1c90/url"; got != want {
			t.Errorf("url = %q, want %q", got, want)
		}
		if got, want := names["logGroup"][0].Name, "/aws/lambda/ocel-app-shop-prod-web-index-r3f8a1c90"; got != want {
			t.Errorf("log group = %q, want %q", got, want)
		}
		if got, want := names["role"][0].Name, "ocel-app-shop-prod-web-role-r3f8a1c90"; got != want {
			t.Errorf("role = %q, want %q", got, want)
		}
	})

	t.Run("the description names project, env, app, bundle and release", func(t *testing.T) {
		t.Parallel()

		coord := Coordinate("shop", testStack(t, "prod", "web"), "fn--web--bundle-0")

		got := string(component.Describe(coord, "bundle bundle-0"))
		if want := "shop / prod / web - bundle bundle-0 - release r3f8a1c90"; got != want {
			t.Fatalf("description = %q, want %q", got, want)
		}
	})

	t.Run("an over-long description is clamped to what Lambda accepts", func(t *testing.T) {
		t.Parallel()

		coord := Coordinate("shop", testStack(t, "prod", "web"), "fn--web--index")

		got := string(component.Describe(coord, "bundle "+strings.Repeat("segment-", 60)))
		if len(got) > component.MaxDescriptionLen {
			t.Errorf("description is %d chars, over the %d-character limit", len(got), component.MaxDescriptionLen)
		}
	})
}

func TestResourceTags(t *testing.T) {
	t.Parallel()

	t.Run("a function is tagged with its component and bundle", func(t *testing.T) {
		t.Parallel()

		tags := component.Tags(naming.KindFunction, "bundle-0", nil)
		want := pulumi.StringMap{"ocel:component": pulumi.String("function"), "ocel:bundle": pulumi.String("bundle-0")}
		if !maps.Equal(tags, want) {
			t.Errorf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("transform tags join the tags ocel writes itself", func(t *testing.T) {
		t.Parallel()

		tags := component.Tags(naming.KindFunction, "bundle-0", map[string]string{"acme:team": "platform"})

		if got := tags["acme:team"]; got != pulumi.String("platform") {
			t.Errorf("acme:team = %v, want the transform's value", got)
		}
		if got := tags[component.TagComponent]; got != pulumi.String(naming.KindFunction.Component()) {
			t.Errorf("%s = %v, want ocel's own component tag", component.TagComponent, got)
		}
		if got := tags[component.TagBundle]; got != pulumi.String("bundle-0") {
			t.Errorf("%s = %v, want ocel's own bundle tag", component.TagBundle, got)
		}
	})

	t.Run("a transform cannot displace a tag ocel writes itself", func(t *testing.T) {
		t.Parallel()

		tags := component.Tags(naming.KindFunction, "bundle-0", map[string]string{
			component.TagComponent: "mine",
			component.TagBundle:    "mine",
		})

		if got := tags[component.TagComponent]; got != pulumi.String(naming.KindFunction.Component()) {
			t.Errorf("%s = %v, want ocel's own value kept", component.TagComponent, got)
		}
		if got := tags[component.TagBundle]; got != pulumi.String("bundle-0") {
			t.Errorf("%s = %v, want ocel's own value kept", component.TagBundle, got)
		}
	})

	t.Run("a role has no bundle", func(t *testing.T) {
		t.Parallel()

		tags := component.Tags(naming.KindRole, "", nil)
		want := pulumi.StringMap{component.TagComponent: pulumi.String("role")}
		if !maps.Equal(tags, want) {
			t.Errorf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("the keys agree with the coordinate's own tag set", func(t *testing.T) {
		t.Parallel()

		coord := Coordinate("shop", testStack(t, "prod", "web"), "fn--web--bundle-0")
		all := coord.Tags(naming.TagValues{Bundle: "bundle-0"})
		if got, want := all[component.TagComponent], "function"; got != want {
			t.Errorf("Tags[%s] = %q, want %q", component.TagComponent, got, want)
		}
		if got, want := all[component.TagBundle], "bundle-0"; got != want {
			t.Errorf("Tags[%s] = %q, want %q", component.TagBundle, got, want)
		}
	})
}

func TestAFunctionNamesTheBundleItWasBuiltFromNotARoute(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	program := func(pctx *pulumi.Context) error {
		stack := testStack(t, "prod", "web")
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{Namespace: testNamespace, App: "web", Boundary: componenttest.BoundaryARN})
		if err != nil {
			return err
		}
		args, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			return err
		}
		_, err = registerFunction(pctx, "fn--web--bundle-0", Coordinate("shop", stack, "fn--web--bundle-0"), namedFunction(stack, "fn--web--bundle-0"),
			"bundle-0", args, Artifact{Bucket: "artifacts", Key: "fn.zip"},
			nil, nil, nil, nil, role.Arn, pulumi.StringArray{pulumi.String(mockAccountARN("lambda", "layer:runtime:1"))}, functionurl.AuthIAM, "")
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	fn := rec.inputs("aws:lambda/function:Function", "ocel-app-shop-prod-web-bundle-0-r3f8a1c90")
	if len(fn) == 0 {
		t.Fatal("no function was registered")
	}
	tags := fn["tags"].ObjectValue()
	if got := tags["ocel:bundle"]; !got.IsString() || got.StringValue() != "bundle-0" {
		t.Errorf("tags[ocel:bundle] = %v, want the bundle id %q", got, "bundle-0")
	}
	if got, set := tags["ocel:route"]; set {
		t.Errorf("tags[ocel:route] = %v, want no route tag: one bundle serves many routes", got)
	}
	if got, want := fn["description"].StringValue(), "shop / prod / web - bundle bundle-0 - release r3f8a1c90"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}

	logs := rec.inputs("aws:cloudwatch/logGroup:LogGroup", "/aws/lambda/"+namedFunction(testStack(t, "prod", "web"), "fn--web--bundle-0").Pulumi)
	if got := logs["tags"].ObjectValue()["ocel:bundle"]; !got.IsString() || got.StringValue() != "bundle-0" {
		t.Errorf("log group tags[ocel:bundle] = %v, want the bundle id %q", got, "bundle-0")
	}
}

func TestFunctionEnv(t *testing.T) {
	t.Run("without cache", func(t *testing.T) {
		base := map[string]string{"OCEL_RESOURCE_POSTGRES_main": "{}"}

		env := Env(base, Args{Handler: "src/server.js"}, nil, nil)

		want := map[string]string{
			"OCEL_RESOURCE_POSTGRES_main": "{}",
			"AWS_LAMBDA_EXEC_WRAPPER":     execWrapper,
			"OCEL_HANDLER":                "/var/task/src/server.js",
		}
		if !maps.Equal(env, want) {
			t.Errorf("functionEnv(base, args, nil, nil) = %v, want %v", env, want)
		}
	})

	t.Run("a node runtime function with no isr still takes the bytecode cache", func(t *testing.T) {
		cache := Bytecode{Bucket: "artifacts-xyz", Prefix: "bytecode/proj/prod/api/API1"}

		env := Env(map[string]string{}, Args{Handler: "index.mjs"}, nil, &cache)

		if got := env["OCEL_BYTECODE_PREFIX"]; got != cache.Prefix {
			t.Errorf("OCEL_BYTECODE_PREFIX = %q, want %q", got, cache.Prefix)
		}
		if got := env["OCEL_BYTECODE_BUCKET"]; got != cache.Bucket {
			t.Errorf("OCEL_BYTECODE_BUCKET = %q, want %q", got, cache.Bucket)
		}
		if _, ok := env["OCEL_STORAGE_COORDINATE"]; ok {
			t.Error("OCEL_STORAGE_COORDINATE is set on a function with no ISR")
		}
	})

	t.Run("has no bytecode cache where the spec names none", func(t *testing.T) {
		env := Env(map[string]string{}, Args{Handler: "index.mjs"}, nil, nil)

		for _, key := range []string{"OCEL_BYTECODE_PREFIX", "OCEL_BYTECODE_BUCKET"} {
			if _, ok := env[key]; ok {
				t.Errorf("%s = %q, want it unset for an app the spec gives no cache", key, env[key])
			}
		}
	})

	t.Run("leaves base untouched", func(t *testing.T) {
		base := map[string]string{"OCEL_RESOURCE_BUCKET_uploads": "{}"}

		Env(base, Args{Handler: "src/server.js"}, &site.ISR{Coord: storageCoordinate("prod", "proj", "web", deployedAs("B1").Token())}, nil)

		if got := slices.Sorted(maps.Keys(base)); !slices.Equal(got, []string{"OCEL_RESOURCE_BUCKET_uploads"}) {
			t.Errorf("base keys = %v, want only OCEL_RESOURCE_BUCKET_uploads", got)
		}
	})
}

func TestAFunctionsEnvCountsResourcesVariablesAndTheRuntimeTogether(t *testing.T) {
	t.Run("accounts resources, variables and the runtime together", func(t *testing.T) {
		base := map[string]string{
			"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://x"}`,
			"POSTHOG_ID":                  "ph-123",
		}

		env := Env(base, Args{Handler: "src/server.js"}, &site.ISR{Coord: storageCoordinate("prod", "proj", "web", deployedAs("B1").Token())}, nil)

		for _, key := range []string{"OCEL_RESOURCE_POSTGRES_main", "POSTHOG_ID", "OCEL_HANDLER", "AWS_LAMBDA_EXEC_WRAPPER", "OCEL_STORAGE_COORDINATE"} {
			if _, ok := env[key]; !ok {
				t.Errorf("env is missing %s; the budget would be charged against less than is deployed", key)
			}
		}
		if _, ok := base["OCEL_HANDLER"]; ok {
			t.Error("functionEnv mutated the shared base env")
		}
	})
}

func TestEveryTransformKeyOfAPublicFunctionNamesAResourceItsProgramRegisters(t *testing.T) {
	t.Parallel()

	rec := &inputRecorder{}
	stack := testStack(t, "prod", "web")
	program := func(pctx *pulumi.Context) error {
		role, err := NewRole(pctx, component.AppRoleCoordinate("shop", stack), ExecutionRole{Namespace: testNamespace, App: "web", Boundary: componenttest.BoundaryARN})
		if err != nil {
			return err
		}
		args, err := NewArgs("", provider.FunctionSpec{})
		if err != nil {
			return err
		}
		_, err = registerFunction(pctx, "fn--web--index", Coordinate("shop", stack, "fn--web--index"), namedFunction(stack, "fn--web--index"),
			"index", args, Artifact{Bucket: "artifacts", Key: "fn.zip"},
			nil, nil, nil, nil, role.Arn, pulumi.StringArray{pulumi.String(mockAccountARN("lambda", "layer:runtime:1"))}, functionurl.AuthNone, "")
		return err
	}
	if err := pulumi.RunErr(program, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run program: %v", err)
	}

	for key, refs := range mustResourceNames(t, stack, "fn--web--index") {
		for _, ref := range refs {
			if !slices.Contains(rec.registered(ref.Token), ref.Name) {
				t.Errorf("transform key %s names %s %q, and the program registers %v", key, ref.Token, ref.Name, rec.registered(ref.Token))
			}
		}
	}
	if permissions := mustResourceNames(t, stack, "fn--web--index")["urlPermission"]; len(permissions) != len(functionurl.Public()) {
		t.Errorf("urlPermission names %v, want every permission a public URL is granted through: a patch to one alone leaves the URL answering 403", permissions)
	}
}
