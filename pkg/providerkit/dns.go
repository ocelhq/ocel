package providerkit

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type DNSRegistry interface {
	Supported() []DNSKind

	Default() DNSKind

	Open(kind DNSKind, zone string) (edge.DNSWriter, error)
}

type DNSKind string
