package discovery

import (
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestTheFixturesOfAnotherLanguageHoldNoJS(t *testing.T) {
	for _, name := range []string{
		filepath.Join("deploy", "go"),
		filepath.Join("deploy", "python"),
		filepath.Join("sdk", "go"),
		filepath.Join("sdk", "python"),
		filepath.Join("sdk", "rust"),
	} {
		t.Run(name, func(t *testing.T) {
			dir := repoFixture(t, name)
			held, err := HoldsJS(&projectconfig.Config{Dir: dir})
			if err != nil {
				t.Fatalf("HoldsJS: %v", err)
			}
			if held {
				t.Fatalf("%s holds js, and its own language is not js", dir)
			}
		})
	}
}
