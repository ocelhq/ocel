package host

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	sizeFields  = "{{.MemTotal}} {{.NCPU}}"
	sizeCommand = "docker info --format '" + sizeFields + "'"
)

const minimumMemoryBytes = 256 << 20

type boxSize struct {
	MemoryBytes int64
	CPUs        int
}

type Limits struct {
	MemoryBytes int64
	CPUs        string
}

type sizing struct {
	mu    sync.Mutex
	known bool
	size  boxSize
}

func (h *Host) readBoxSize(ctx context.Context) (boxSize, error) {
	h.sizing.mu.Lock()
	defer h.sizing.mu.Unlock()
	if h.sizing.known {
		return h.sizing.size, nil
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return boxSize{}, err
	}
	said, err := h.ran(ctx, "read how much memory and how many CPUs the engine has", sizeCommand, nil, elevation)
	if err != nil {
		return boxSize{}, err
	}
	size, err := readSize(said)
	if err != nil {
		return boxSize{}, refusal.Refuse(refusal.CodeNotReady,
			"%s answered %q for its memory and CPUs, so no container can be given a limit: %v", h.named(), strings.TrimSpace(said), err)
	}
	h.sizing.known, h.sizing.size = true, size
	return size, nil
}

func readSize(said string) (boxSize, error) {
	fields := strings.Fields(said)
	if len(fields) != 2 {
		return boxSize{}, refusal.Refuse(refusal.CodeInvalid, "want the memory in bytes and the CPU count")
	}
	memory, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || memory <= 0 {
		return boxSize{}, refusal.Refuse(refusal.CodeInvalid, "%q is not a count of bytes", fields[0])
	}
	cpus, err := strconv.Atoi(fields[1])
	if err != nil || cpus <= 0 {
		return boxSize{}, refusal.Refuse(refusal.CodeInvalid, "%q is not a count of CPUs", fields[1])
	}
	return boxSize{MemoryBytes: memory, CPUs: cpus}, nil
}

func limitsFor(tier environment.Tier, size boxSize) Limits {
	if tier == environment.TierPreview {
		return Limits{MemoryBytes: share(size, 4), CPUs: strconv.Itoa(max(1, size.CPUs/2))}
	}
	return Limits{MemoryBytes: share(size, 2)}
}

func share(size boxSize, parts int64) int64 {
	return min(size.MemoryBytes, max(minimumMemoryBytes, size.MemoryBytes/parts))
}

func (l Limits) argv() []string {
	if l.MemoryBytes == 0 {
		return nil
	}
	argv := []string{"--memory", strconv.FormatInt(l.MemoryBytes, 10)}
	if l.CPUs != "" {
		argv = append(argv, "--cpus", l.CPUs)
	}
	return argv
}

func (h *Host) tierLimits(ctx context.Context, tier environment.Tier) (Limits, error) {
	size, err := h.readBoxSize(ctx)
	if err != nil {
		return Limits{}, err
	}
	return limitsFor(tier, size), nil
}
