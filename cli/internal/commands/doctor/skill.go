package doctor

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/skill"
)

const skillFix = "run `ocel skill install --yes`"

func skillChecks(root, cliVersion string) []check {
	installed := false
	var stale []string
	for _, dir := range skill.Dirs(root) {
		stamped, err := skill.ReadVersion(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		installed = true
		relative, _ := filepath.Rel(root, dir)
		if err != nil {
			return []check{{verdict: verdictWarn, text: "ocel skill in " + relative + " is unreadable — " + firstLine(err.Error()), fix: skillFix}}
		}
		if stamped != "" && stamped != cliVersion {
			stale = append(stale, relative+" is for ocel "+stamped)
		}
	}
	switch {
	case !installed:
		return nil
	case len(stale) > 0:
		return []check{{verdict: verdictWarn, text: "ocel skill is stale — " + strings.Join(stale, ", ") + ", this CLI is " + cliVersion, fix: skillFix}}
	default:
		return []check{{verdict: verdictPass, text: "ocel skill installed"}}
	}
}
