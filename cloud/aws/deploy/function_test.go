package deploy

import (
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
