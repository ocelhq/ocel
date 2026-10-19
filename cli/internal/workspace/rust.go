package workspace

import (
	"fmt"
	"path"

	"github.com/ocelhq/ocel/cli/internal/shellword"
)

const (
	rustBinDir    = "bin"
	rustTargetDir = "target"
)

func (l Location) planRustCommands() Commands {
	if l.Entrypoint == "" {
		return Commands{}
	}
	bin := l.Entrypoint
	build := fmt.Sprintf("mkdir -p %s && cargo build --release --bin %s --target-dir %s && cp %s %s/", rustBinDir, shellword.Quote(bin), rustTargetDir, shellword.Quote(path.Join(rustTargetDir, "release", bin)), rustBinDir)
	return Commands{Build: l.inAppDir(build), Start: shellword.Quote("./" + path.Join(l.Path, rustBinDir, bin))}
}
