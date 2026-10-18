package cloudflare

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

const defaultNamespace = "ocel"

var truncationMarker = regexp.MustCompile(`-x[0-9a-f]{8}$`)

func TestEntryWorkerName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, "ocel--shop--prod--root"},
		{environment.TierPreview, "ocel--shop--preview--root"},
	} {
		got, err := entryWorkerName(defaultNamespace, "shop", tc.tier)
		if err != nil {
			t.Fatalf("entryWorkerName(%s): %v", tc.tier, err)
		}
		if got != tc.want {
			t.Errorf("entryWorkerName(%s) = %q, want %q", tc.tier, got, tc.want)
		}
	}
	if _, err := entryWorkerName(defaultNamespace, "shop", environment.Tier("nonsense")); err == nil {
		t.Error("entryWorkerName(unknown tier) err = nil, want an error")
	}
}

func TestProjectOwnsScriptReadsOnlyThisNamespacesWorkers(t *testing.T) {
	t.Parallel()

	const other = "j-1874-deploy-next-cloudflare"
	theirs, err := entryWorkerName(other, "shop", environment.TierProduction)
	if err != nil {
		t.Fatalf("entryWorkerName: %v", err)
	}
	if projectOwnsScript(defaultNamespace, "shop")(theirs) {
		t.Errorf("%q reads as this namespace's, so a prune here would take a route off the worker %s deploys", theirs, other)
	}
	if !projectOwnsScript(other, "shop")(theirs) {
		t.Errorf("%q does not read as %s's own worker", theirs, other)
	}
}

func TestProjectOwnsWorkerAgreesWithProjectOwnsScript(t *testing.T) {
	t.Parallel()

	for script, want := range map[string]bool{
		"ocel--shop--prod--web":    true,
		"ocel--shopfoo--prod--web": false,
		"my-worker":                false,
	} {
		if got := ProjectOwnsWorker(defaultNamespace, "shop", script); got != want {
			t.Errorf("ProjectOwnsWorker(shop, %q) = %v, want %v", script, got, want)
		}
		if got := projectOwnsScript(defaultNamespace, "shop")(script); got != want {
			t.Errorf("projectOwnsScript(shop)(%q) = %v, want %v", script, got, want)
		}
	}
	for _, args := range [][3]string{{"", "shop", "ocel--shop--prod--web"}, {defaultNamespace, "", "ocel--shop--prod--web"}, {defaultNamespace, "shop", ""}} {
		if ProjectOwnsWorker(args[0], args[1], args[2]) {
			t.Errorf("ProjectOwnsWorker(%q, %q, %q) = true, want false for an empty argument", args[0], args[1], args[2])
		}
	}
}

func TestWorkerScriptName(t *testing.T) {
	t.Run("every boundary is one field separator", func(t *testing.T) {
		t.Parallel()
		if got, want := workerScriptName(defaultNamespace, "shop", "prod", "web"), "ocel--shop--prod--web"; got != want {
			t.Errorf("workerScriptName = %q, want %q", got, want)
		}
		if got := workerScriptName(defaultNamespace, "shop", "prod", "web"); truncationMarker.MatchString(got) {
			t.Errorf("%q is marked truncated but fits", got)
		}
	})

	t.Run("environments that differ past the truncation point keep distinct names", func(t *testing.T) {
		t.Parallel()
		slug := strings.Repeat("verylongproject", 5)
		short := workerScriptName(defaultNamespace, slug, "pr-7", "web")
		long := workerScriptName(defaultNamespace, slug, "pr-71", "web")

		for _, name := range []string{short, long} {
			if len(name) > maxWorkerNameLength {
				t.Errorf("%q is %d chars, over the %d-char limit", name, len(name), maxWorkerNameLength)
			}
			if !truncationMarker.MatchString(name) {
				t.Errorf("%q was truncated without saying so", name)
			}
		}
		if short == long {
			t.Fatalf("pr-7 and pr-71 deploy over one another as %q", short)
		}
	})

	t.Run("apps in one environment keep distinct names", func(t *testing.T) {
		t.Parallel()
		slug := strings.Repeat("verylongproject", 5)
		if web, docs := workerScriptName(defaultNamespace, slug, "prod", "web"), workerScriptName(defaultNamespace, slug, "prod", "docs"); web == docs {
			t.Fatalf("two apps collided on one script name: %q", web)
		}
	})
}
