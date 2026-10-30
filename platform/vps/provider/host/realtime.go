package host

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

const (
	realtimeBinaryName = "ocel-realtime"
	RealtimeDir        = boxapi.LibDir + "/realtime"
	RealtimeBinary     = RealtimeDir + "/" + realtimeBinaryName
	RealtimeMount      = "/ocel/realtime"
	RealtimeMounted    = RealtimeMount + "/" + realtimeBinaryName
)

func newRealtimeItem(assets Assets, arch string) machine.Item {
	return machine.Item{Kind: machine.KindFile, Name: RealtimeBinary, Mode: 0o755, Owner: machine.RootOwner, Content: assets.binary(realtimeBinaryName, arch),
		Note: "serves each environment's realtime channels"}
}

func RealtimeItems(assets Assets, arch string) []machine.Item {
	return []machine.Item{machine.Dir(RealtimeDir, 0o755, machine.RootOwner, ""), newRealtimeItem(assets, arch)}
}

func (h *Host) IsRealtimeCurrent(ctx context.Context) (bool, error) {
	return h.isCurrent(ctx, "survey the realtime gateway", func(arch string) machine.Item { return newRealtimeItem(h.assets, arch) })
}
