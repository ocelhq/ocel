package routing

import (
	"context"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Table struct {
	box   *ssh.Box
	front proxy.Proxy
	pins  func(context.Context) ([]boxapi.Pin, error)
}

func NewTable(box *ssh.Box, front proxy.Proxy, pins func(context.Context) ([]boxapi.Pin, error)) *Table {
	return &Table{box: box, front: front, pins: pins}
}

func WriteRoutingTable(table boxapi.RoutingTable) ([]byte, error) {
	written, err := boxapi.WriteRoutingTable(table)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%v", err)
	}
	return written, nil
}

func ReadRoutingTable(document []byte) (boxapi.RoutingTable, error) {
	table, err := boxapi.ReadRoutingTable(document)
	if err != nil {
		return boxapi.RoutingTable{}, unrenderable(err)
	}
	return table, nil
}

func unrenderable(err error) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s contains what ocel cannot render into %s: %v",
		boxapi.RoutingTablePath, boxapi.ProxyConfig, err)
}
