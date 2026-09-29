package runui

import (
	"fmt"

	"github.com/fatih/color"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

const missingIndent = "  "

func MissingVariablesHeadline(n int) string {
	if n == 1 {
		return "1 variable is not ready — nothing has been built."
	}
	return fmt.Sprintf("%d variables are not ready — nothing has been built.", n)
}

func MissingVariablesLines(missing *streamv1.MissingVariables, present Presentation) []string {
	fail := func(text string) string { return colorFor(present, color.FgRed).Sprint(text) }
	cells := missing.GetCells()
	out := []string{fail(failMark) + " " + MissingVariablesHeadline(len(cells)), ""}
	keyWidth, folderWidth := 0, 0
	for _, cell := range cells {
		width := len(cell.GetKey())
		if cell.GetGroup() != "" {
			width += len(missingIndent)
		}
		keyWidth = max(keyWidth, width)
		folderWidth = max(folderWidth, len(VariableFolderName(cell.GetFolder())))
	}

	written := map[string]bool{}
	for _, cell := range cells {
		group := cell.GetGroup()
		if group == "" {
			out = append(out, missingVariableLines(cell, missingIndent, keyWidth, folderWidth, present)...)
			continue
		}
		if written[group] {
			continue
		}
		written[group] = true
		out = append(out, missingIndent+muted(present, VariableGroupHeadline(group, missingGroupDescription(missing.GetGroups(), group))))
		for _, member := range cells {
			if member.GetGroup() == group {
				out = append(out, missingVariableLines(member, missingIndent+missingIndent, keyWidth-len(missingIndent), folderWidth, present)...)
			}
		}
	}
	return out
}

func missingVariableLines(cell *streamv1.MissingVariable, lead string, keyWidth, folderWidth int, present Presentation) []string {
	key := fmt.Sprintf("%-*s", keyWidth, cell.GetKey())
	folder := fmt.Sprintf("%-*s", folderWidth, VariableFolderName(cell.GetFolder()))
	out := []string{lead + colorFor(present, color.FgRed).Sprint(failMark) + " " + key + missingIndent + muted(present, folder) + missingIndent + cell.GetReason()}
	if description := cell.GetDescription(); description != "" {
		out = append(out, lead+missingIndent+muted(present, description))
	}
	return out
}

func missingGroupDescription(groups []*streamv1.MissingGroup, key string) string {
	for _, group := range groups {
		if group.GetKey() == key {
			return group.GetDescription()
		}
	}
	return ""
}

func VariableGroupHeadline(group, description string) string {
	out := group + " — set together"
	if description != "" {
		out += " (" + description + ")"
	}
	return out
}

func MissingVariablesRemedy(remedy string) string {
	return missingIndent + "Fill them in: " + remedy
}

func VariableDescriptionLine(description string) string {
	if description == "" {
		return ""
	}
	return "\n" + missingIndent + description
}

func VariableFolderName(folder string) string {
	if folder == "" {
		return "root"
	}
	return folder
}
