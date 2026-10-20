package terminal

import (
	"github.com/ocelhq/ocel/cli/internal/variables/wording"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func jobInputLines(waiting *streamv1.WaitingEvent, palette Palette) []string {
	lines := []string{"The job needs your input."}
	where := "Answer it on Ocel Cloud at:"
	if confirmation := waiting.GetInput().GetConfirmation(); confirmation != nil {
		lines = nil
		if finding := confirmation.GetFinding(); finding != "" {
			lines = append(lines, EscapeControls(finding))
		}
		lines = append(lines, "Ocel Cloud asks: "+EscapeControls(confirmation.GetQuestion()))
	} else if len(waiting.GetMissing().GetCells()) > 0 {
		lines = wording.Lines(waiting.GetMissing(), palette)
		where = "Fill them in on Ocel Cloud at:"
	}
	if waiting.GetUrl() == "" {
		return append(lines, "", blockIndent+"Waiting for Ocel Cloud to answer.")
	}
	return append(lines,
		"",
		blockIndent+where,
		"",
		blockIndent+blockIndent+waiting.GetUrl(),
		"",
		blockIndent+"Waiting for Ocel Cloud — press Ctrl-C to stop watching; the job waits there either way.",
	)
}
