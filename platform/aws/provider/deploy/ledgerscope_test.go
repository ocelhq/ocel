package deploy

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
)

func TestTheLedgerPartitionNamesTheProjectTheISRPrefixDoes(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"shop", "Shop Ltd", "shop_2", "SHOP--2"} {
		coord := storageCoordinate("prod", slug, "web", deployedAs("BUILD1").Token())
		project := strings.Split(isrPrefixOf(coord), naming.PathSeparator)[1]

		if got := ledger.Partition(environment.TierProduction, slug).Path; !slices.Equal(got, []string{project}) {
			t.Errorf("Partition(%q).Path = %q, want [%q]; the invalidator reads the ledger under the project the ISR prefix names, so a partition that differs makes every raise miss", slug, got, project)
		}
	}
}
