package deploy

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
)

func TestTheLedgerPartitionNamesTheProjectTheStorageCoordinateDoes(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{"shop", "Shop Ltd", "shop_2", "SHOP--2"} {
		coord := storageCoordinate("prod", slug, "web", deployedAs("BUILD1").Token())
		project := coord.Project

		if got := ledger.Partition(environment.TierProduction, slug).Path; !slices.Equal(got, []string{project}) {
			t.Errorf("Partition(%q).Path = %q, want [%q]; the invalidator reads the ledger under the project the storage coordinate names, so a partition that differs makes every raise miss", slug, got, project)
		}
	}
}
