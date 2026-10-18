package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	realtimeBinaryName = "ocel-realtime"
	RealtimeDir        = boxapi.LibDir + "/realtime"
	RealtimeBinary     = RealtimeDir + "/" + realtimeBinaryName
	RealtimeMount      = "/ocel/realtime"
	RealtimeMounted    = RealtimeMount + "/" + realtimeBinaryName
)

func realtimeBinary(arch string) []byte { return embedded(realtimeBinaryName, arch) }

var RealtimeBinarySum = sync.OnceValue(func() string {
	sum := sha256.New()
	for _, arch := range []string{ssh.AMD64, ssh.ARM64} {
		sum.Write(realtimeBinary(arch))
	}
	return hex.EncodeToString(sum.Sum(nil))
})

func newRealtimeItem(arch string) machine.Item {
	return machine.Item{Kind: machine.KindFile, Name: RealtimeBinary, Mode: 0o755, Owner: machine.RootOwner, Content: realtimeBinary(arch),
		Note: "serves each environment's realtime channels"}
}

func RealtimeItems(arch string) []machine.Item {
	return []machine.Item{machine.Dir(RealtimeDir, 0o755, machine.RootOwner, ""), newRealtimeItem(arch)}
}

func (h *Host) IsRealtimeCurrent(ctx context.Context) (bool, error) {
	return h.isCurrent(ctx, "survey the realtime gateway", newRealtimeItem)
}
