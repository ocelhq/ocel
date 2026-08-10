package discovery

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

func Run(ctx context.Context, entry, serverURL string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "node", "--enable-source-maps", entry)
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+serverURL)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	return nil
}
