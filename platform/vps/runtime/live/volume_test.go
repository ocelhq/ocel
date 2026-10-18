package live

import (
	"strings"
	"testing"

	connect "connectrpc.com/connect"
)

func TestAStoreVolumeRefusesWritesBelowATenthFreeOrAGibibyteWhicheverIsMore(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		free, total uint64
		refused     bool
	}{
		{"a large volume under a tenth free", 200 << 20, 100 << 30, true},
		{"a large volume with half free", 50 << 30, 100 << 30, false},
		{"a small volume under a gibibyte free", 900 << 20, 4 << 30, true},
		{"a small volume over a gibibyte free", 2 << 30, 4 << 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := refuseBelowFloor(tc.free, tc.total)
			if !tc.refused {
				if err != nil {
					t.Fatalf("refuseBelowFloor() = %v, want the write let through", err)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "free") {
				t.Fatalf("refuseBelowFloor() = %v, want the write refused as resource exhausted, saying how much is free", err)
			}
		})
	}
}
