package telemetry

import (
	"os"
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

func StartFlush(executable string, resolution Resolution) bool {
	if !resolution.IsCollecting() || !isBannerShown(userconfig.Read()) {
		return false
	}
	spool, err := OpenSpool()
	if err != nil || !spool.HasEvents() {
		return false
	}
	err = childprocess.StartDetached(func() *exec.Cmd {
		cmd := exec.Command(executable, "telemetry", "flush")
		cmd.Dir = os.TempDir()
		return cmd
	})
	return err == nil
}
