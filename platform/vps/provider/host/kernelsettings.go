package host

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
)

const (
	overcommitPath           = "/proc/sys/vm/overcommit_memory"
	transparentHugePagesPath = "/sys/kernel/mm/transparent_hugepage/enabled"

	kernelSettingsCommand = "cat " + overcommitPath + " 2>/dev/null || echo; cat " + transparentHugePagesPath + " 2>/dev/null || true"
)

func kernelWarnings(output string) []string {
	lines := strings.Split(output, "\n")
	var warnings []string
	if overcommit := strings.TrimSpace(lines[0]); overcommit != "" && overcommit != "1" {
		warnings = append(warnings, "vm.overcommit_memory is "+overcommit+" on this box, and a kv store's background save can fail without it: "+
			"run `sysctl vm.overcommit_memory=1` and add `vm.overcommit_memory = 1` to /etc/sysctl.conf")
	}
	if len(lines) > 1 && strings.Contains(lines[1], "[always]") {
		warnings = append(warnings, "transparent huge pages are always on in this box's kernel, which Valkey logs against for the latency and memory a fork costs a kv store: "+
			"run `echo madvise > "+transparentHugePagesPath+"` and set it at boot")
	}
	return warnings
}

func (h *Host) warnOfKernelSettings(ctx context.Context, progress progress.Log) {
	if progress == nil {
		return
	}
	output, err := h.ran(ctx, "read the kernel settings Valkey checks", kernelSettingsCommand, nil, "")
	if err != nil {
		return
	}
	for _, warning := range kernelWarnings(output) {
		progress.Warn(warning)
	}
}
