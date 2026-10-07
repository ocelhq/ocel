package project

import (
	"strings"
	"testing"
	"time"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestALifecyclePointWrittenAsAStringIsACommandWithTheDefaults(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","lifecycle":{"preBuild":"pnpm drizzle-kit migrate"}}`)

	got := cfg.Lifecycle.PreBuild
	if got == nil {
		t.Fatal("Lifecycle.PreBuild = nil, want the command")
	}
	if got.Command != "pnpm drizzle-kit migrate" || got.App != "" || got.Previews != PreviewsPersistent || got.Timeout != DefaultLifecycleTimeout {
		t.Errorf("PreBuild = %+v, want the command run on persistent previews with the default timeout and no app", got)
	}
}

func TestALifecyclePointWrittenAsAnObjectKeepsEachOfItsFields(t *testing.T) {
	t.Parallel()

	cfg := mustLoadJSON(t, `{"slug":"shop","apps":[{"name":"web","path":"web"}],"lifecycle":{"preBuild":{
		"command":"pnpm migrate","app":"web","previews":"all","timeout":"90s"
	}}}`)

	want := LifecycleCommand{Command: "pnpm migrate", App: "web", Previews: PreviewsAll, Timeout: 90 * time.Second}
	if got := cfg.Lifecycle.PreBuild; got == nil || *got != want {
		t.Errorf("PreBuild = %+v, want %+v", got, want)
	}
}

func TestAProjectWithoutALifecycleHasNoPreBuild(t *testing.T) {
	t.Parallel()

	if cfg := mustLoadJSON(t, `{"slug":"shop"}`); cfg.Lifecycle.PreBuild != nil {
		t.Errorf("PreBuild = %+v, want none", cfg.Lifecycle.PreBuild)
	}
}

func TestALifecycleCommandThatCannotRunIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		config string
		want   string
	}{
		"an empty command":                            {`{"slug":"shop","lifecycle":{"preBuild":"  "}}`, "lifecycle.preBuild"},
		"an object with no command":                   {`{"slug":"shop","lifecycle":{"preBuild":{"previews":"all"}}}`, "command"},
		"a previews policy that is none of the three": {`{"slug":"shop","lifecycle":{"preBuild":{"command":"x","previews":"sometimes"}}}`, "persistent, all or none"},
		"a timeout that is not a duration":            {`{"slug":"shop","lifecycle":{"preBuild":{"command":"x","timeout":"soon"}}}`, "timeout"},
		"a timeout of zero":                           {`{"slug":"shop","lifecycle":{"preBuild":{"command":"x","timeout":"0s"}}}`, "timeout"},
		"a negative timeout":                          {`{"slug":"shop","lifecycle":{"preBuild":{"command":"x","timeout":"-5m"}}}`, "timeout"},
		"an app the project does not have":            {`{"slug":"shop","apps":[{"name":"web","path":"web"}],"lifecycle":{"preBuild":{"command":"x","app":"api"}}}`, `"api"`},
		"a point the config does not have":            {`{"slug":"shop","lifecycle":{"postBuild":"x"}}`, "postBuild"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := loadJSON(t, c.config)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load err = %v, want it to name %q", err, c.want)
			}
		})
	}
}

func TestAPreBuildRunsOnTheEnvironmentsItsPreviewsPolicyNames(t *testing.T) {
	t.Parallel()

	production := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}
	persistent := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT}
	ephemeral := &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL}

	cases := []struct {
		previews Previews
		on       [3]bool
	}{
		{PreviewsPersistent, [3]bool{true, true, false}},
		{PreviewsAll, [3]bool{true, true, true}},
		{PreviewsNone, [3]bool{true, false, false}},
	}
	for _, c := range cases {
		command := LifecycleCommand{Command: "x", Previews: c.previews}
		for i, env := range []*environmentv1.Environment{production, persistent, ephemeral} {
			if got := command.RunsIn(env); got != c.on[i] {
				t.Errorf("previews %q runs in %v = %v, want %v", c.previews, env, got, c.on[i])
			}
		}
	}
}
