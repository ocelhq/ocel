package docker

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	SizeFields  = "{{.MemTotal}} {{.NCPU}}"
	sizeCommand = "docker info --format '" + SizeFields + "'"
)

const MinimumMemoryBytes = 256 << 20

type boxSize struct {
	MemoryBytes int64
	CPUs        int
}

type Limits struct {
	MemoryBytes int64
	CPUs        string
}

type Engine struct {
	box *ssh.Box

	mu    sync.Mutex
	known bool
	size  boxSize
}

func NewEngine(box *ssh.Box) *Engine { return &Engine{box: box} }

func (e *Engine) readBoxSize(ctx context.Context) (boxSize, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.known {
		return e.size, nil
	}
	said, err := e.box.Run(ctx, ssh.Docker, "read how much memory and how many CPUs the engine has", sizeCommand, nil)
	if err != nil {
		return boxSize{}, err
	}
	size, err := readSize(said)
	if err != nil {
		return boxSize{}, refusal.Refuse(refusal.CodeNotReady,
			"%s answered %q for its memory and CPUs, so no container can be given a limit: %v", e.box.Named(), strings.TrimSpace(said), err)
	}
	e.known, e.size = true, size
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
	return min(size.MemoryBytes, max(MinimumMemoryBytes, size.MemoryBytes/parts))
}

func (l Limits) Argv() []string {
	if l.MemoryBytes == 0 {
		return nil
	}
	argv := []string{"--memory", strconv.FormatInt(l.MemoryBytes, 10)}
	if l.CPUs != "" {
		argv = append(argv, "--cpus", l.CPUs)
	}
	return argv
}

func (e *Engine) Limits(ctx context.Context, tier environment.Tier) (Limits, error) {
	size, err := e.readBoxSize(ctx)
	if err != nil {
		return Limits{}, err
	}
	return limitsFor(tier, size), nil
}
