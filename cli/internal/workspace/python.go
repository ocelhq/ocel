package workspace

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/cli/internal/shellword"
)

func (l Location) planPythonCommands() (Commands, error) {
	if l.Entrypoint == "" {
		return Commands{}, nil
	}
	resolved, err := pythonserver.ResolveEntrypoint(l.Dir(), l.Entrypoint)
	if err != nil {
		return Commands{}, fmt.Errorf("plan the python app's start command: %w", err)
	}
	words := resolved.Command()
	for i, word := range words {
		words[i] = shellword.Quote(word)
	}
	return Commands{Start: l.inAppDir(strings.Join(words, " ")), Asset: Asset{Name: pythonserver.File, Content: pythonserver.Source}}, nil
}
