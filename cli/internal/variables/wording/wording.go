package wording

import (
	"fmt"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

const (
	indent    = "  "
	failGlyph = "✗"
)

type Style interface {
	Failure(text string) string
	Muted(text string) string
}

type Plain struct{}

func (Plain) Failure(text string) string { return text }

func (Plain) Muted(text string) string { return text }

func VariablesAre(n int) string {
	if n == 1 {
		return "1 variable is"
	}
	return fmt.Sprintf("%d variables are", n)
}

func Headline(n int) string {
	return VariablesAre(n) + " not ready — nothing has been built."
}

func Lines(missing *streamv1.MissingVariables, style Style) []string {
	cells := missing.GetCells()
	out := []string{style.Failure(failGlyph) + " " + Headline(len(cells)), ""}
	keyWidth, folderWidth := 0, 0
	for _, cell := range cells {
		width := len(cell.GetKey())
		if cell.GetGroup() != "" {
			width += len(indent)
		}
		keyWidth = max(keyWidth, width)
		folderWidth = max(folderWidth, len(FolderName(cell.GetFolder())))
	}

	written := map[string]bool{}
	for _, cell := range cells {
		group := cell.GetGroup()
		if group == "" {
			out = append(out, cellLines(cell, indent, keyWidth, folderWidth, style)...)
			continue
		}
		if written[group] {
			continue
		}
		written[group] = true
		out = append(out, indent+style.Muted(GroupHeadline(group, groupDescription(missing.GetGroups(), group))))
		for _, member := range cells {
			if member.GetGroup() == group {
				out = append(out, cellLines(member, indent+indent, keyWidth-len(indent), folderWidth, style)...)
			}
		}
	}
	return out
}

func cellLines(cell *streamv1.MissingVariable, lead string, keyWidth, folderWidth int, style Style) []string {
	key := fmt.Sprintf("%-*s", keyWidth, cell.GetKey())
	folder := fmt.Sprintf("%-*s", folderWidth, FolderName(cell.GetFolder()))
	out := []string{lead + style.Failure(failGlyph) + " " + key + indent + style.Muted(folder) + indent + cell.GetReason()}
	if description := cell.GetDescription(); description != "" {
		out = append(out, lead+indent+style.Muted(description))
	}
	return out
}

func groupDescription(groups []*streamv1.MissingGroup, key string) string {
	for _, group := range groups {
		if group.GetKey() == key {
			return group.GetDescription()
		}
	}
	return ""
}

func GroupHeadline(group, description string) string {
	out := group + " — set together"
	if description != "" {
		out += " (" + description + ")"
	}
	return out
}

func Remedy(remedy string) string {
	return indent + "Fill them in: " + remedy
}

func DescriptionLine(description string) string {
	if description == "" {
		return ""
	}
	return "\n" + indent + description
}

func FolderName(folder string) string {
	if folder == "" {
		return "root"
	}
	return folder
}
