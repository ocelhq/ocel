package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type DNS interface {
	Supported() []DNSKind

	Open(kind DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error)
}

type DNSKind string
