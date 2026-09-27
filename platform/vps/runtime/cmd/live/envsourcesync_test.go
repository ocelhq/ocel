package main

import (
	"strings"
	"testing"
)

func TestTheEnvSourceSyncRefusesAClassThisBoxCannotBootstrap(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{nil, {"--class", "staging"}, {"--class", "../production"}} {
		var said strings.Builder
		if code := envSourceSync(argv, &said); code != 2 {
			t.Errorf("envsourcesync %q exited %d, want 2: a sync over no class would poll records no deploy writes", argv, code)
		}
		if !strings.Contains(said.String(), "production or preview") {
			t.Errorf("envsourcesync %q said %q, and never names the classes it takes", argv, said.String())
		}
	}
}
