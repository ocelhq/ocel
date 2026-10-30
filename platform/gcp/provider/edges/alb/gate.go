package alb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

type PluginImages interface {
	Ensure(ctx context.Context, tier environment.Tier, plugin []byte) (string, error)

	Delete(ctx context.Context, tier environment.Tier) error
}

func newGateVersion(image, config string) albresources.GateVersion {
	digest := sha256.Sum256([]byte(image + "\n" + config))
	return albresources.GateVersion{ID: "v" + hex.EncodeToString(digest[:8]), Image: image, Config: config}
}
