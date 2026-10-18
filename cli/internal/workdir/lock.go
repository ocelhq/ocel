package workdir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const (
	locksDirName        = "locks"
	buildOutputLockName = "output"
	lockRetry           = 250 * time.Millisecond
)

func lock(ctx context.Context, projectDir, name string, waiting func()) (release func(), err error) {
	dir := filepath.Join(projectDir, buildoutput.StateDir, locksDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	held := flock.New(filepath.Join(dir, name+".lock"))
	taken, err := held.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", held.Path(), err)
	}
	if !taken {
		waiting()
		if taken, err = held.TryLockContext(ctx, lockRetry); err != nil {
			return nil, fmt.Errorf("wait for %s: %w", held.Path(), err)
		}
		if !taken {
			return nil, fmt.Errorf("wait for %s: %w", held.Path(), ctx.Err())
		}
	}
	return func() { _ = held.Unlock() }, nil
}

func LockBuildOutput(ctx context.Context, projectDir string, waiting func()) (release func(), err error) {
	return lock(ctx, projectDir, buildOutputLockName, waiting)
}
