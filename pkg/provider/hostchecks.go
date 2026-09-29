package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
)

type HostVerdict int

const (
	HostPass HostVerdict = iota
	HostNeedsAction
	HostFail
)

type HostCheck struct {
	Subject string
	Verdict HostVerdict
	Finding string
	Fix     string
}

type HostCheckRequest struct {
	Tier      environment.Tier
	Edge      edge.Kind
	Hostnames []string
}
