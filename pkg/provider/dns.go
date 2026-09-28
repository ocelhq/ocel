package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
)

type DNS interface {
	Open(kind DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error)
}

type DNSKind string
