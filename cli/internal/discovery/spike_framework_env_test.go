package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

// Spike only: bundles a fixture's env file exactly as discovery does and prints the output path.
func TestSpikeBundleFrameworkEnv(t *testing.T) {
	dir := os.Getenv("SPIKE_APP_DIR")
	file := os.Getenv("SPIKE_ENV_FILE")
	if dir == "" || file == "" {
		t.Skip("SPIKE_APP_DIR and SPIKE_ENV_FILE unset")
	}
	out, err := Bundle(dir, []string{filepath.Join(dir, file)})
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}
	t.Logf("bundled to %s", out)
}
