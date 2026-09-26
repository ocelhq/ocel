package provider

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type HostVerdict int

const (
	HostPass HostVerdict = iota
	HostOwed
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
