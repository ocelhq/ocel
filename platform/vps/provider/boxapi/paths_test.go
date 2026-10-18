package boxapi_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func TestTheRoutingTableIsNeverWhereTheProxyCanReadIt(t *testing.T) {
	t.Parallel()

	if strings.HasPrefix(boxapi.RoutingTable, boxapi.ProxyDir+"/") {
		t.Errorf("the routing table lives at %s, inside %s, which the proxy container mounts", boxapi.RoutingTable, boxapi.ProxyDir)
	}
}
