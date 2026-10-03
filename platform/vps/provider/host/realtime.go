package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

const (
	realtimeBinaryName = "ocel-realtime"
	RealtimeDir        = boxstore.Dir + "/realtime"
	RealtimeBinary     = RealtimeDir + "/" + realtimeBinaryName
	RealtimeMount      = "/ocel/realtime"
	RealtimeMounted    = RealtimeMount + "/" + realtimeBinaryName
	LabelBinary        = "ocel.binary"
)

func realtimeBinary(arch string) []byte { return embedded(realtimeBinaryName, arch) }

var RealtimeBinarySum = sync.OnceValue(func() string {
	sum := sha256.New()
	for _, arch := range []string{ArchAMD64, ArchARM64} {
		sum.Write(realtimeBinary(arch))
	}
	return hex.EncodeToString(sum.Sum(nil))
})

func newRealtimeItem(arch string) Item {
	return Item{Kind: KindFile, Name: RealtimeBinary, Mode: 0o755, Owner: rootOwner, Content: realtimeBinary(arch),
		Note: "serves each environment's realtime channels"}
}

func RealtimeItems(arch string) []Item {
	return []Item{dir(RealtimeDir, 0o755, rootOwner, ""), newRealtimeItem(arch)}
}

func (h *Host) IsRealtimeCurrent(ctx context.Context) (bool, error) {
	return h.isCurrent(ctx, "survey the realtime gateway", newRealtimeItem)
}
