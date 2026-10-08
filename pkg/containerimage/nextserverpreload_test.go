package containerimage_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

func TestANextContainerRunsNodeWithTheServerPreloadAfterTheNodeOptionsItIsGiven(t *testing.T) {
	t.Parallel()

	preload := "--import /ocel/runtime/next/server-preload.mjs"
	for _, tc := range []struct {
		name string
		env  []string
		want []string
	}{
		{name: "no node options", env: []string{"PATH=/usr/bin"}, want: []string{"PATH=/usr/bin", "NODE_OPTIONS=" + preload}},
		{name: "node options of the image or the deploy", env: []string{"NODE_OPTIONS=--max-old-space-size=512", "PATH=/usr/bin"}, want: []string{"NODE_OPTIONS=--max-old-space-size=512 " + preload, "PATH=/usr/bin"}},
		{name: "empty node options", env: []string{"NODE_OPTIONS="}, want: []string{"NODE_OPTIONS=" + preload}},
	} {
		got := containerimage.AppendNextServerPreload(holding(containerimage.NextServerPreloadPath), tc.env)
		if !slices.Equal(got, tc.want) {
			t.Errorf("AppendNextServerPreload(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAContainerWithoutTheNextServerPreloadKeepsItsNodeOptions(t *testing.T) {
	t.Parallel()

	for _, env := range [][]string{
		{"PATH=/usr/bin"},
		{"NODE_OPTIONS=--max-old-space-size=512", "PATH=/usr/bin"},
	} {
		if got := containerimage.AppendNextServerPreload(holding(), env); !slices.Equal(got, env) {
			t.Errorf("AppendNextServerPreload(%q) = %q, want the environment untouched", env, got)
		}
	}
}

func TestAppendingTheNextServerPreloadLeavesTheEnvironmentItWasGiven(t *testing.T) {
	t.Parallel()

	env := []string{"NODE_OPTIONS=--a"}

	containerimage.AppendNextServerPreload(holding(containerimage.NextServerPreloadPath), env)

	if env[0] != "NODE_OPTIONS=--a" {
		t.Errorf("env[0] = %q, want it unchanged", env[0])
	}
}
