//go:build unix && !linux

package childprocess

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

const psSnapshotTimeout = 2 * time.Second

func descendantPIDs(pid int) []int {
	ctx, cancel := context.WithTimeout(context.Background(), psSnapshotTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=").Output() //nolint:forbidigo // signalling a process tree has no run context to record the snapshot in
	if err != nil {
		return nil
	}
	return descendantsOf(parsePPIDTable(bytes.NewReader(out)), pid)
}
