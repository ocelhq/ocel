package discovery

import (
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestTheFixturesOfAnotherLanguageHoldNoJS(t *testing.T) {
	tried := 0
	for _, dir := range fixturetest.Dirs(t) {
		if fixturetest.IsNode(t, dir) {
			continue
		}
		tried++
		t.Run(filepath.Base(filepath.Dir(dir))+"/"+filepath.Base(dir), func(t *testing.T) {
			held, err := HoldsJS(&projectconfig.Config{Dir: dir})
			if err != nil {
				t.Fatalf("HoldsJS: %v", err)
			}
			if held {
				t.Fatalf("%s holds js, and its own language is not js", dir)
			}
		})
	}
	if tried == 0 {
		t.Fatal("no fixture is written in a language other than js")
	}
}
