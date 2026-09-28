package provider

import (
	"github.com/ocelhq/ocel/pkg/edge"
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
	Class     edge.Class
	Hostnames []string
}
