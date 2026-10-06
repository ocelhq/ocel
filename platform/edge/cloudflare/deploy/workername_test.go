package cloudflare

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

const defaultNamespace = "ocel"

var truncationMarker = regexp.MustCompile(`-x[0-9a-f]{8}$`)

func TestConventionWorkerNames(t *testing.T) {
	t.Parallel()

	t.Run("production names every worker family the project deploys", func(t *testing.T) {
		t.Parallel()

		got, err := conventionWorkerNames(defaultNamespace, "shop", environment.TierProduction, []string{"web"})
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		assertSet(t, "names", got, []string{
			"ocel--shop--prod--root",
			"ocel--shop--prod--web",
		})
	})

	t.Run("preview names the worker the preview tier deploys", func(t *testing.T) {
		t.Parallel()

		got, err := conventionWorkerNames(defaultNamespace, "shop", environment.TierPreview, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		assertSet(t, "names", got, []string{"ocel--shop--preview--root"})
	})

	t.Run("another namespace deploying the same slug derives its own names", func(t *testing.T) {
		t.Parallel()

		mine, err := conventionWorkerNames(defaultNamespace, "shop", environment.TierProduction, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		theirs, err := conventionWorkerNames("j-1874-deploy-next-cloudflare", "shop", environment.TierProduction, nil)
		if err != nil {
			t.Fatalf("conventionWorkerNames: %v", err)
		}
		for _, name := range theirs {
			for _, ours := range mine {
				if name == ours {
					t.Errorf("a sweep of this namespace names %q, which is the worker another namespace deploys for the same slug", name)
				}
			}
		}
	})

	t.Run("a state that names no namespace, tier or slug derives nothing", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			namespace string
			slug      string
			tier      environment.Tier
		}{
			{namespace: defaultNamespace, slug: "", tier: environment.TierProduction},
			{namespace: defaultNamespace, slug: "shop", tier: ""},
			{namespace: "", slug: "shop", tier: environment.TierProduction},
		} {
			got, err := conventionWorkerNames(tc.namespace, tc.slug, tc.tier, []string{"web"})
			if err != nil {
				t.Fatalf("conventionWorkerNames(%q, %q, %q): %v", tc.namespace, tc.slug, tc.tier, err)
			}
			if len(got) != 0 {
				t.Errorf("names = %v, want none: nothing identifies the project's workers", got)
			}
		}
	})

	t.Run("an unknown tier is an error", func(t *testing.T) {
		t.Parallel()

		if _, err := conventionWorkerNames(defaultNamespace, "shop", environment.Tier("nonsense"), nil); err == nil {
			t.Error("conventionWorkerNames(unknown tier) err = nil, want an error")
		}
	})
}

func TestProjectOwnsScriptReadsOnlyThisNamespacesWorkers(t *testing.T) {
	t.Parallel()

	const other = "j-1874-deploy-next-cloudflare"
	theirs, err := conventionWorkerNames(other, "shop", environment.TierProduction, nil)
	if err != nil {
		t.Fatalf("conventionWorkerNames: %v", err)
	}
	owns := projectOwnsScript(defaultNamespace, "shop")
	for _, name := range theirs {
		if owns(name) {
			t.Errorf("%q reads as this namespace's, so a prune here would take a route off the worker %s deploys", name, other)
		}
	}
	if !projectOwnsScript(other, "shop")(theirs[0]) {
		t.Errorf("%q does not read as %s's own worker", theirs[0], other)
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
		if got, want := rootWorkerName(defaultNamespace, "shop", "prod"), "ocel--shop--prod--root"; got != want {
			t.Errorf("rootWorkerName = %q, want %q", got, want)
		}
		if got, want := previewWorkerName(defaultNamespace, "shop"), "ocel--shop--preview--root"; got != want {
			t.Errorf("previewWorkerName = %q, want %q", got, want)
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
			if len(name) > maxWorkerNameLen {
				t.Errorf("%q is %d chars, over the %d-char limit", name, len(name), maxWorkerNameLen)
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

func TestThePreviewWorkerFamilySitsUnderItsOwnStem(t *testing.T) {
	t.Parallel()
	stem := previewWorkerStem(defaultNamespace, "shop")
	if !edge.NameUnderStem(stem, previewWorkerName(defaultNamespace, "shop")) {
		t.Errorf("%q is not under the preview stem %q", previewWorkerName(defaultNamespace, "shop"), stem)
	}
	if edge.NameUnderStem(stem, rootWorkerName(defaultNamespace, "shop", "prod")) {
		t.Errorf("the production root worker sits under the preview stem %q", stem)
	}
}
