package provider

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type DNS interface {
	Open(kind DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error)
}

type DNSKind string
