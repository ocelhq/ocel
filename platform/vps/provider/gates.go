package vps

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
)

func newRecordedGate(store records.Store, cipher seal.Cipher) box.RecordedGate {
	return func(ctx context.Context, tier environment.Tier, project string) (*gate.Gate, error) {
		records, err := stackrecords.ReadGates(ctx, store, cipher, tier)
		if err != nil {
			return nil, err
		}
		return records[project].GateInForce(), nil
	}
}
