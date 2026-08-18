package deploy

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/edgeledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestLedgerScopeNamesTheProjectTheISRPrefixDoes(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"shop", "Shop Ltd", "shop_2", "SHOP--2"} {
		coord := storageCoordinate("prod", slug, "web", releaseOf(deployedAs("BUILD1")))
		project := strings.Split(isrPrefixOf(coord), naming.PathSeparator)[1]
		want := string(edge.ClassProduction) + naming.PathSeparator + project

		if got := edgeledger.Scope(edge.ClassProduction, slug); got != want {
			t.Errorf("Scope(%q) = %q, want %q; the invalidator reads the ledger under the project the ISR prefix names, so a scope that differs makes every raise miss", slug, got, want)
		}
	}
}
