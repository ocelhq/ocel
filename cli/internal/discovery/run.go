package discovery

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Run executes a bundled discovery entrypoint against the server at serverURL —
// a dev server for `ocel dev`, a deploy collector for `ocel deploy`. Both
// phases declare through the same SDK, so the flags and the variable names that
// SDK reads live here and nowhere else. --enable-source-maps is what lets a
// declaration name the file the user wrote rather than the bundle.
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
