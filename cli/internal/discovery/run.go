package discovery

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/obs"
)

func Run(ctx context.Context, entry, serverURL string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "node", "--enable-source-maps", entry)
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+serverURL)
	cmd.Stderr = stderr

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	proc := &nodeprotocol.Processor{Run: obs.FromContext(ctx), Forward: stdout}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	proc.Scan(ctx, pipe)
	runErr := cmd.Wait()

	if runErr != nil {
		proc.Abort()
		if msg := proc.Err(); msg != "" {
			return fmt.Errorf("discovery failed (%w): %s", runErr, msg)
		}
		return fmt.Errorf("discovery failed: %w", runErr)
	}
	return nil
}
