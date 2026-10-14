package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

func newRecordedGate(store keyvalue.Store, cipher seal.Cipher) box.RecordedGate {
	return func(ctx context.Context, tier environment.Tier, project string) (*gate.Gate, error) {
		return stackrecords.ReadGate(ctx, store, cipher, tier, project)
	}
}
