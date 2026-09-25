package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type DNSRegistry interface {
	Supported() []DNSKind

	Open(kind DNSKind, zone string, front edge.Kind) (edge.DNSWriter, error)
}

type DNSKind string
