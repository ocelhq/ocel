package buildoutput

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
)

func TestHostingRoundTripsNeeds(t *testing.T) {
	t.Parallel()

	raw := `{"version":1,"framework":"next","frameworkBuildId":"b1","rootFunction":"bundle-0","routeTable":"next",` +
		`"static":{"immutablePrefixes":["/docs/_next/static/"],"mustRevalidatePrefixes":["/docs/_next/static/service-worker/"]},"needs":{` +
		`"edge-middleware":{"count":1,"matchers":["^/dashboard(?:/(.*))?$"]},` +
		`"edge-runtime":{"count":2,"routes":["/edgy","/api/stream"]},` +
		`"ppr-resume":{"count":1,"routes":["/"]},` +
		`"edge-cache":{"count":3},` +
		`"streaming":{"count":2}}}`

	var hosting Hosting
	if err := json.Unmarshal([]byte(raw), &hosting); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := Hosting{
		Version:          HostingVersion,
		Framework:        "next",
		FrameworkBuildID: "b1",
		RootFunction:     "bundle-0",
		RouteTable:       RouteTableNext,
		Static: &Static{
			ImmutablePrefixes:      []string{"/docs/_next/static/"},
			MustRevalidatePrefixes: []string{"/docs/_next/static/service-worker/"},
		},
		Needs: map[edge.Need]NeedDetail{
			edge.NeedEdgeMiddleware: {Count: 1, Matchers: []string{"^/dashboard(?:/(.*))?$"}},
			edge.NeedEdgeRuntime:    {Count: 2, Routes: []string{"/edgy", "/api/stream"}},
			edge.NeedPPRResume:      {Count: 1, Routes: []string{"/"}},
			edge.NeedEdgeCache:      {Count: 3},
			edge.NeedStreaming:      {Count: 2},
		},
	}
	if !reflect.DeepEqual(hosting, want) {
		t.Fatalf("hosting = %+v, want %+v", hosting, want)
	}

	encoded, err := json.Marshal(hosting)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again Hosting
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("unmarshal again: %v", err)
	}
	if !reflect.DeepEqual(again, hosting) {
		t.Fatalf("round trip = %+v, want %+v", again, hosting)
	}
}

func TestHostingKeepsAnUnknownNeedName(t *testing.T) {
	t.Parallel()

	var hosting Hosting
	if err := json.Unmarshal([]byte(`{"framework":"next","needs":{"time-travel":{"count":1}}}`), &hosting); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := hosting.Needs["time-travel"]; !ok {
		t.Fatalf("needs = %+v, want the unknown name kept for the origin to refuse", hosting.Needs)
	}
}

func TestHostingWithNoRouteTableOrStaticDirWritesNeitherKey(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(Hosting{Version: HostingVersion, Framework: "node", RootFunction: "/", Needs: map[edge.Need]NeedDetail{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"routeTable", "static"} {
		if _, ok := keys[key]; ok {
			t.Errorf("hosting = %s, want no %q key for a build that has none", encoded, key)
		}
	}
}

func TestAStaticPathIsImmutableOnlyUnderAnImmutablePrefixAndOutsideEveryMustRevalidatePrefix(t *testing.T) {
	t.Parallel()

	static := &Static{
		ImmutablePrefixes:      []string{"/docs/_next/static/"},
		MustRevalidatePrefixes: []string{"/docs/_next/static/service-worker/"},
	}
	for path, want := range map[string]bool{
		"/docs/_next/static/chunks/main.js":       true,
		"/docs/_next/static/service-worker/sw.js": false,
		"/_next/static/chunks/main.js":            false,
		"/docs/favicon.ico":                       false,
	} {
		if got := static.IsImmutable(path); got != want {
			t.Errorf("IsImmutable(%q) = %v, want %v", path, got, want)
		}
	}
	if (*Static)(nil).IsImmutable("/docs/_next/static/chunks/main.js") {
		t.Error("a build with no static dir calls a path immutable")
	}
}
