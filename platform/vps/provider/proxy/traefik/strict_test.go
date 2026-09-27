package traefik

import (
	"os"
	"strings"
	"testing"
)

func TestOcelsOwnRenderDecodesStrictlyAndAKeyTraefikDoesNotKnowIsRefused(t *testing.T) {
	t.Parallel()

	own, err := os.ReadFile("testdata/wildcard.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := strictly(own); err != nil {
		t.Errorf("strictly(testdata/wildcard.yml) = %v, want the render ocel writes read back whole", err)
	}
	unknown := strings.Replace(string(own), "      priority: 1000000\n", "      priority: 1000000\n      weight: 3\n", 1)
	if err := strictly([]byte(unknown)); err == nil || !strings.Contains(err.Error(), "weight") {
		t.Errorf("strictly(a router with weight) = %v, want it refused naming the key: Traefik refuses a file with a key it does not know, and every file in the directory with it", err)
	}
}
