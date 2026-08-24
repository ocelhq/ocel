package deploy

import (
	"encoding/json"
	"maps"
	"path"
	"regexp"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestWorkerOutputName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{"web": "web-worker", "Web_1": "web-1-worker"}
	for in, want := range cases {
		if got := workerOutputName(in); got != want {
			t.Errorf("workerOutputName(%q) = %q, want %q", in, got, want)
		}
	}
}

var truncationMarker = regexp.MustCompile(`-x[0-9a-f]{8}$`)

func TestWorkerScriptName(t *testing.T) {
	t.Run("every boundary is one field separator", func(t *testing.T) {
		t.Parallel()
		if got, want := workerScriptName("shop", "prod", "web"), "ocel--shop--prod--web"; got != want {
			t.Errorf("workerScriptName = %q, want %q", got, want)
		}
		if got, want := rootWorkerName("shop", "prod"), "ocel--shop--prod--root"; got != want {
			t.Errorf("rootWorkerName = %q, want %q", got, want)
		}
		if got, want := previewWorkerName("shop"), "ocel--shop--preview--root"; got != want {
			t.Errorf("previewWorkerName = %q, want %q", got, want)
		}
		if got := workerScriptName("shop", "prod", "web"); truncationMarker.MatchString(got) {
			t.Errorf("%q is marked truncated but fits", got)
		}
	})

	t.Run("environments that differ past the truncation point keep distinct names", func(t *testing.T) {
		t.Parallel()
		slug := strings.Repeat("verylongproject", 5)
		short := workerScriptName(slug, "pr-7", "web")
		long := workerScriptName(slug, "pr-71", "web")

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
		if web, docs := workerScriptName(slug, "prod", "web"), workerScriptName(slug, "prod", "docs"); web == docs {
			t.Fatalf("two apps collided on one script name: %q", web)
		}
	})
}

func TestProjectWorkerStems(t *testing.T) {
	t.Parallel()

	t.Run("a project owns both its own and its retired names", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			script string
			want   bool
		}{
			{workerScriptName("shop", "prod", "web"), true},
			{previewWorkerName("shop"), true},
			{"ocel-shop--prod-web", true},
			{"ocel-shop--preview", true},
			{workerScriptName("shopfoo", "prod", "web"), false},
			{workerScriptName("shop-preview", "prod", "web"), false},
			{"my-worker", false},
		}
		for _, tc := range cases {
			if got := ProjectOwnsWorker("shop", tc.script); got != tc.want {
				t.Errorf("ProjectOwnsWorker(shop, %q) = %v, want %v", tc.script, got, tc.want)
			}
		}
	})

	t.Run("the preview family sits under its own stem", func(t *testing.T) {
		t.Parallel()
		stem := previewWorkerStem("shop")
		if !edge.NameUnderStem(stem, previewWorkerName("shop")) {
			t.Errorf("%q is not under the preview stem %q", previewWorkerName("shop"), stem)
		}
		if edge.NameUnderStem(stem, rootWorkerName("shop", "prod")) {
			t.Errorf("the production root worker sits under the preview stem %q", stem)
		}
	})
}

func serveDescriptor(t *testing.T, framework, buildID string) string {
	t.Helper()
	raw, err := json.Marshal(edge.ServeDescriptor{Framework: framework, BuildID: buildID, Entry: "/"})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func buildIDOf(t *testing.T, routingManifest string) string {
	t.Helper()
	var routing struct {
		BuildID string `json:"buildId"`
	}
	if err := json.Unmarshal([]byte(routingManifest), &routing); err != nil {
		t.Fatalf("parse routing manifest %s: %v", routingManifest, err)
	}
	return routing.BuildID
}

func withServeDescriptors(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	out := maps.Clone(files)
	for rel, contents := range files {
		app, ok := appOfRoutingManifest(rel)
		if !ok {
			continue
		}
		descriptor := path.Join(appsDirName, app, edge.ServeDescriptorFile)
		if _, written := out[descriptor]; written {
			continue
		}
		out[descriptor] = serveDescriptor(t, frameworkNext, buildIDOf(t, contents))
	}
	return out
}

func appOfRoutingManifest(rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 || parts[0] != appsDirName || parts[2] != edge.RoutingManifestFile {
		return "", false
	}
	return parts[1], true
}
