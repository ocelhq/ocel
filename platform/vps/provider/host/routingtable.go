package host

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

type RoutingTable = boxapi.RoutingTable

func WriteRoutingTable(table RoutingTable) ([]byte, error) {
	written, err := boxapi.WriteRoutingTable(table)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return written, nil
}

func ReadRoutingTable(document []byte) (RoutingTable, error) {
	table, err := boxapi.ReadRoutingTable(document)
	if err != nil {
		return RoutingTable{}, unrenderable(err)
	}
	return table, nil
}

func unrenderable(err error) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s contains what ocel cannot render into %s: %v",
		boxapi.RoutingTablePath, ProxyConfig, err)
}
