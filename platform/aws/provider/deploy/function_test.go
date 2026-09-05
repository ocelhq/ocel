package deploy

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
)

func fixedRelease(t *testing.T) naming.Release {
	t.Helper()
	release, err := naming.ParseRelease("r3f8a1c90")
	if err != nil {
		t.Fatalf("parse release: %v", err)
	}
	return release
}

func testStack(t *testing.T, env, app string) naming.StackName {
	t.Helper()
	return naming.AppStack(env, app, fixedRelease(t))
}

func TestFunctionCoordinate(t *testing.T) {
	t.Parallel()

	t.Run("the physical name carries project, env, app, route and release", func(t *testing.T) {
		t.Parallel()

		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--api-users")

		got := coord.PhysicalName(maxLambdaBaseNameLen)
		if want := "shop-prod-web-api-users-r3f8a1c90"; got != want {
			t.Fatalf("PhysicalName = %q, want %q", got, want)
		}
		for _, fact := range []string{"shop", "prod", "web", "api-users", "r3f8a1c90"} {
			if !strings.Contains(got, fact) {
				t.Errorf("PhysicalName = %q, missing %q", got, fact)
			}
		}
	})

	t.Run("a long route erodes before the project, env, app or release", func(t *testing.T) {
		t.Parallel()

		route := strings.Repeat("very-long-route-segment-", 6)
		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--"+route)

		got := coord.PhysicalName(maxLambdaBaseNameLen)
		if len(got)+lambdaAutonameSuffixLen > maxLambdaNameLen {
			t.Fatalf("PhysicalName = %q (%d chars) leaves no room for the %d-character suffix within %d", got, len(got), lambdaAutonameSuffixLen, maxLambdaNameLen)
		}
		if !strings.HasPrefix(got, "shop-prod-web-") {
			t.Errorf("PhysicalName = %q, want the project, env and app kept intact", got)
		}
		if !strings.Contains(got, "very-long-route") {
			t.Errorf("PhysicalName = %q, want what survives of the route", got)
		}
	})

	t.Run("the resource id reads as kind then local name", func(t *testing.T) {
		t.Parallel()

		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--index")

		if got, want := coord.PhysicalName(maxLambdaBaseNameLen), "shop-prod-web-index-r3f8a1c90"; got != want {
			t.Errorf("function resource id = %q, want %q", got, want)
		}
		if got, want := naming.ResourceID(naming.KindFunction, coord.Name, "url"), "fn-index-url"; got != want {
			t.Errorf("url resource id = %q, want %q", got, want)
		}
	})

	t.Run("the description names project, env, app, route and release", func(t *testing.T) {
		t.Parallel()

		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--api-users")

		got := string(describe(coord, "route /api/users"))
		if want := "shop / prod / web - route /api/users - release r3f8a1c90"; got != want {
			t.Fatalf("description = %q, want %q", got, want)
		}
	})

	t.Run("an over-long description is clamped to what Lambda accepts", func(t *testing.T) {
		t.Parallel()

		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--index")

		got := string(describe(coord, "route /"+strings.Repeat("segment/", 60)))
		if len(got) > maxDescriptionLen {
			t.Errorf("description is %d chars, over the %d-character limit", len(got), maxDescriptionLen)
		}
	})
}

func TestRoleCoordinate(t *testing.T) {
	t.Parallel()

	coord := roleCoordinate("shop", testStack(t, "prod", "web"))

	if got, want := rolePrefix(coord), "shop-prod-web-app-role-r3f8a1c90-"; got != want {
		t.Errorf("rolePrefix = %q, want %q", got, want)
	}
	if got, want := naming.ResourceID(naming.KindRole, roleLocalName), "role-app"; got != want {
		t.Errorf("resource id = %q, want %q", got, want)
	}
	for _, tc := range []struct {
		parts []string
		want  string
	}{
		{[]string{"policy", "logs"}, "role-app-policy-logs"},
		{[]string{"policy", "isr", "cache"}, "role-app-policy-isr-cache"},
		{[]string{"policy", "vars", "read"}, "role-app-policy-vars-read"},
	} {
		if got := naming.ResourceID(naming.KindRole, roleLocalName, tc.parts...); got != tc.want {
			t.Errorf("resource id = %q, want %q", got, tc.want)
		}
	}
	if got, want := string(describe(coord, "execution role for this app's functions")),
		"shop / prod / web - execution role for this app's functions - release r3f8a1c90"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

func TestRolePrefixLeavesRoomForTheSuffix(t *testing.T) {
	t.Parallel()

	long := roleCoordinate(strings.Repeat("p", 30), naming.AppStack(strings.Repeat("e", 30), strings.Repeat("a", 30), fixedRelease(t)))

	got := rolePrefix(long)
	if len(got)+iamAutonameSuffixLen > maxRoleNameLen {
		t.Errorf("rolePrefix = %q (%d chars) leaves no room for the %d-character suffix within %d", got, len(got), iamAutonameSuffixLen, maxRoleNameLen)
	}
	if !strings.HasSuffix(got, naming.WordSeparator) {
		t.Errorf("rolePrefix = %q, want it to end on the word separator", got)
	}
}

func TestResourceTags(t *testing.T) {
	t.Parallel()

	t.Run("a function is tagged with its component and route", func(t *testing.T) {
		t.Parallel()

		tags := resourceTags(naming.KindFunction, "/api/users", nil)
		want := pulumi.StringMap{tagComponent: pulumi.String("function"), tagRoute: pulumi.String("/api/users")}
		if !maps.Equal(tags, want) {
			t.Errorf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("transform tags join the tags ocel writes itself", func(t *testing.T) {
		t.Parallel()

		tags := resourceTags(naming.KindFunction, "/api/users", map[string]string{"acme:team": "platform"})

		if got := tags["acme:team"]; got != pulumi.String("platform") {
			t.Errorf("acme:team = %v, want the transform's value", got)
		}
		if got := tags[tagComponent]; got != pulumi.String(naming.KindFunction.Component()) {
			t.Errorf("%s = %v, want ocel's own component tag", tagComponent, got)
		}
		if got := tags[tagRoute]; got != pulumi.String("/api/users") {
			t.Errorf("%s = %v, want ocel's own route tag", tagRoute, got)
		}
	})

	t.Run("a transform cannot displace a tag ocel writes itself", func(t *testing.T) {
		t.Parallel()

		tags := resourceTags(naming.KindFunction, "/api/users", map[string]string{
			tagComponent: "mine",
			tagRoute:     "mine",
		})

		if got := tags[tagComponent]; got != pulumi.String(naming.KindFunction.Component()) {
			t.Errorf("%s = %v, want ocel's own value to stand", tagComponent, got)
		}
		if got := tags[tagRoute]; got != pulumi.String("/api/users") {
			t.Errorf("%s = %v, want ocel's own value to stand", tagRoute, got)
		}
	})

	t.Run("a role carries no route", func(t *testing.T) {
		t.Parallel()

		tags := resourceTags(naming.KindRole, "", nil)
		want := pulumi.StringMap{tagComponent: pulumi.String("role")}
		if !maps.Equal(tags, want) {
			t.Errorf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("the keys agree with the coordinate's own tag set", func(t *testing.T) {
		t.Parallel()

		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--api-users")
		all := coord.Tags(naming.Facts{Route: "/api/users"})
		if got, want := all[tagComponent], "function"; got != want {
			t.Errorf("Tags[%s] = %q, want %q", tagComponent, got, want)
		}
		if got, want := all[tagRoute], "/api/users"; got != want {
			t.Errorf("Tags[%s] = %q, want %q", tagRoute, got, want)
		}
	})
}

func TestFunctionEnv(t *testing.T) {
	t.Run("without cache", func(t *testing.T) {
		base := map[string]string{"OCEL_RESOURCE_POSTGRES_main": "{}"}

		env := functionEnv(base, functionArgs{Handler: "src/server.js"}, nil, nil)

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
		t.Setenv(bytecodeCacheEnv, "1")
		cache := bytecodeConfig{Bucket: "assets-xyz", Prefix: "prod/proj/api/API1/bytecode"}

		env := functionEnv(map[string]string{}, functionArgs{Handler: "index.mjs"}, nil, &cache)

		if got := env["OCEL_BYTECODE_PREFIX"]; got != cache.Prefix {
			t.Errorf("OCEL_BYTECODE_PREFIX = %q, want %q", got, cache.Prefix)
		}
		if got := env["OCEL_BYTECODE_BUCKET"]; got != cache.Bucket {
			t.Errorf("OCEL_BYTECODE_BUCKET = %q, want %q", got, cache.Bucket)
		}
		if _, ok := env["OCEL_ISR_PREFIX"]; ok {
			t.Error("OCEL_ISR_PREFIX is set on a function with no ISR")
		}
	})

	t.Run("carries no bytecode cache with the gate off", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "")

		env := functionEnv(map[string]string{}, functionArgs{Handler: "index.mjs"}, nil, &bytecodeConfig{Bucket: "assets-xyz", Prefix: "prod/proj/api/API1/bytecode"})

		for _, key := range []string{"OCEL_BYTECODE_PREFIX", "OCEL_BYTECODE_BUCKET"} {
			if _, ok := env[key]; ok {
				t.Errorf("%s = %q, want it unset without OCEL_BYTECODE_CACHE=1", key, env[key])
			}
		}
	})

	t.Run("leaves base untouched", func(t *testing.T) {
		base := map[string]string{"OCEL_RESOURCE_BUCKET_uploads": "{}"}

		functionEnv(base, functionArgs{Handler: "src/server.js"}, &isrConfig{Prefix: "prod/proj/web/B1"}, nil)

		if got := slices.Sorted(maps.Keys(base)); !slices.Equal(got, []string{"OCEL_RESOURCE_BUCKET_uploads"}) {
			t.Errorf("base keys = %v, want only OCEL_RESOURCE_BUCKET_uploads", got)
		}
	})
}
