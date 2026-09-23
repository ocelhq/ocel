package ocel_test

import (
	"runtime/debug"
	"testing"

	"ocel.dev"
)

func TestTheSDKReadsItsVersionFromTheBuild(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"no build info", nil, false, ""},
		{"a required release", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{
			{Path: "example.com/other", Version: "v1.0.0"},
			{Path: "ocel.dev", Version: "v0.2.1"},
		}}, true, "v0.2.1"},
		{"a replace with a release", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{
			{Path: "ocel.dev", Version: "v0.2.1", Replace: &debug.Module{Path: "example.com/fork", Version: "v0.2.2"}},
		}}, true, "v0.2.2"},
		{"a replace with a local checkout", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}, Deps: []*debug.Module{
			{Path: "ocel.dev", Version: "v0.2.1", Replace: &debug.Module{Path: "../sdk"}},
		}}, true, ""},
		{"the sdk module itself", &debug.BuildInfo{Main: debug.Module{Path: "ocel.dev", Version: "(devel)"}}, true, "(devel)"},
		{"an app that does not require the sdk", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}}, true, ""},
	}
	for _, c := range cases {
		if got := ocel.VersionIn(c.info, c.ok); got != c.want {
			t.Errorf("%s: versionIn() = %q, want %q", c.name, got, c.want)
		}
	}
}
