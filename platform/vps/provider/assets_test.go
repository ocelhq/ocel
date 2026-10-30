package vps

import (
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/host/assets"
)

var TestAssets = host.NewAssets(assets.FS)
