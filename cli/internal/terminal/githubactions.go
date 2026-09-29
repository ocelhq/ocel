package terminal

import (
	"fmt"
	"regexp"
	"strings"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var annotationCommands = map[progressv1.Level]string{
	progressv1.Level_LEVEL_WARN:  "warning",
	progressv1.Level_LEVEL_ERROR: "error",
}

var workflowData = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A")

var workflowCommandStart = regexp.MustCompile(`(\A|[\r\n])([\t\v\f \x{85}\p{Z}]*)(::|##\[)`)

const commandBreak = "\u200b"

func (s *Transcript) printGroup(header blockLine, body []blockLine) {
	title := header.from.render(Presentation{})
	header.text, header.command = "::group::"+workflowData.Replace(title), true
	s.print(header)
	s.print(body...)
	s.print(blockLine{text: "::endgroup::", command: true})
}

func (s *Transcript) escapeWorkflowCommands(l blockLine) string {
	if !s.present.GitHubActions || l.command {
		return l.text
	}
	return workflowCommandStart.ReplaceAllString(l.text, "${1}${2}"+commandBreak+"${3}")
}

func (s *Transcript) annotate(level progressv1.Level, text string) {
	if command, ok := annotationCommands[level]; ok && s.present.GitHubActions {
		fmt.Fprintf(s.w, "::%s::%s\n", command, workflowData.Replace(text))
	}
}
