package main

import (
	"strings"
	"testing"
)

func TestTheSyncerRefusesAClassThisBoxCannotStand(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{nil, {"--class", "staging"}, {"--class", "../production"}} {
		var said strings.Builder
		if code := envSync(argv, &said); code != 2 {
			t.Errorf("envsync %q exited %d, want 2: a syncer over no class would poll records no deploy writes", argv, code)
		}
		if !strings.Contains(said.String(), "production or preview") {
			t.Errorf("envsync %q said %q, and never names the classes it takes", argv, said.String())
		}
	}
}
