package live

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"
)

const minimumFreeBytes = 1 << 30

func RefuseFullVolume(socket string) func(context.Context) error {
	source := over(socket)
	return func(ctx context.Context) error {
		free, total, err := source.FreeSpace(ctx)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("read the store volume's free space: %w", err))
		}
		return refuseBelowFloor(free, total)
	}
}

func refuseBelowFloor(free, total uint64) error {
	floor := uint64(minimumFreeBytes)
	if tenth := total / 10; tenth > floor {
		floor = tenth
	}
	if free < floor {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("the store volume has %d MiB free, below the %d MiB floor", free>>20, floor>>20))
	}
	return nil
}
