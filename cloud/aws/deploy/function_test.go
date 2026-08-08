package deploy

import (
	"maps"
	"slices"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestOcelTags(t *testing.T) {
	t.Run("stamps app, env and project", func(t *testing.T) {
		tags := ocelTags("web", "prod", "proj")
		want := map[string]pulumi.String{
			tagApp:     "web",
			tagEnv:     "prod",
			tagProject: "proj",
		}
		if len(tags) != len(want) {
			t.Fatalf("got %d tags, want %d: %v", len(tags), len(want), tags)
		}
		for k, v := range want {
			if tags[k] != v {
				t.Errorf("Tags[%s] = %v, want %q", k, tags[k], v)
			}
		}
	})

	// The invoke grant keys on ocel:app alone, so it is the one tag that must
	// always be present; empty env/project are skipped rather than stamped blank.
	t.Run("app is always present, empty env and project are skipped", func(t *testing.T) {
		tags := ocelTags("web", "", "")
		if len(tags) != 1 || tags[tagApp] != pulumi.String("web") {
			t.Errorf("tags = %v, want only %s=web", tags, tagApp)
		}
	})
}

// A nil cache is the ordinary case, not an edge one: appCaches only records a
// cache for a Next app, so every function of a SvelteKit (or any other
// non-Next) app reaches functionEnv with nil — on the pre-provisioning budget
// check, before a single resource is created.
func TestFunctionEnvWithoutCache(t *testing.T) {
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
}

// functionEnv must not write through to the shared base map: base is built once
// per app and handed to every function, so a leaked write would put one
// function's handler and cache coordinates on the next.
func TestFunctionEnvLeavesBaseUntouched(t *testing.T) {
	base := map[string]string{"OCEL_RESOURCE_BUCKET_uploads": "{}"}

	functionEnv(base, functionArgs{Handler: "src/server.js"}, &isrConfig{Prefix: "prod/proj/web/B1"}, nil)

	if got := slices.Sorted(maps.Keys(base)); !slices.Equal(got, []string{"OCEL_RESOURCE_BUCKET_uploads"}) {
		t.Errorf("base keys = %v, want only OCEL_RESOURCE_BUCKET_uploads", got)
	}
}
