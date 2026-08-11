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

		got := coord.PhysicalName(maxLambdaNameLen)
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

		got := coord.PhysicalName(maxLambdaNameLen)
		if len(got) > maxLambdaNameLen {
			t.Fatalf("PhysicalName = %q (%d chars), over the %d-character budget", got, len(got), maxLambdaNameLen)
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

		if got, want := naming.ResourceID(naming.KindFunction, coord.Name), "fn-index"; got != want {
			t.Errorf("resource id = %q, want %q", got, want)
		}
		if got, want := naming.ResourceID(naming.KindFunction, coord.Name, "url"), "fn-index-url"; got != want {
			t.Errorf("url resource id = %q, want %q", got, want)
		}
	})

	t.Run("the description names project, env, app, route and release", func(t *testing.T) {
		t.Parallel()

		coord := functionCoordinate("shop", testStack(t, "prod", "web"), "fn--web--api-users")

		got := string(describe(coord, "route /api/users"))
		if want := "shop / prod / web — route /api/users — release r3f8a1c90"; got != want {
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

	if got, want := coord.PhysicalName(maxRoleNameLen), "shop-prod-web-app-role-r3f8a1c90"; got != want {
		t.Errorf("PhysicalName = %q, want %q", got, want)
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
		"shop / prod / web — execution role for this app's functions — release r3f8a1c90"; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

func TestResourceTags(t *testing.T) {
	t.Parallel()

	t.Run("a function is tagged with its component and route", func(t *testing.T) {
		t.Parallel()

		tags := resourceTags(naming.KindFunction, "/api/users")
		want := pulumi.StringMap{tagComponent: pulumi.String("function"), tagRoute: pulumi.String("/api/users")}
		if !maps.Equal(tags, want) {
			t.Errorf("tags = %v, want %v", tags, want)
		}
	})

	t.Run("a role carries no route", func(t *testing.T) {
		t.Parallel()

		tags := resourceTags(naming.KindRole, "")
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

		env := functionEnv(base, functionArgs{Handler: "src/server.js"}, nil)

		want := map[string]string{
			"OCEL_RESOURCE_POSTGRES_main": "{}",
			"AWS_LAMBDA_EXEC_WRAPPER":     execWrapper,
			"OCEL_HANDLER":                "/var/task/src/server.js",
		}
		if !maps.Equal(env, want) {
			t.Errorf("functionEnv(base, args, nil) = %v, want %v", env, want)
		}
	})

	t.Run("leaves base untouched", func(t *testing.T) {
		base := map[string]string{"OCEL_RESOURCE_BUCKET_uploads": "{}"}

		functionEnv(base, functionArgs{Handler: "src/server.js"}, &isrConfig{Prefix: "prod/proj/web/B1"})

		if got := slices.Sorted(maps.Keys(base)); !slices.Equal(got, []string{"OCEL_RESOURCE_BUCKET_uploads"}) {
			t.Errorf("base keys = %v, want only OCEL_RESOURCE_BUCKET_uploads", got)
		}
	})
}
