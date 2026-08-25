package changeplan

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const BypassEnv = "OCEL_DESTROY_BYPASS_CONFIRMATION"

const (
	stackKind     = "stack"
	edgeKind      = "edge"
	parameterKind = "parameters"
	baselineTag   = "core"
	gutter        = "  "
	typeGutter    = "   "
	slowNote      = " (this one is slow)"
)

func BypassRequest() string {
	return strings.TrimSpace(os.Getenv(BypassEnv))
}

type Printer struct {
	out   io.Writer
	color bool
}

func NewPrinter(out io.Writer) *Printer {
	return newPrinter(out, deployui.IsTerminal(out) && !color.NoColor)
}

func newPrinter(out io.Writer, colorEnabled bool) *Printer {
	return &Printer{out: out, color: colorEnabled}
}

func (p *Printer) Print(header string, plan *contractv1.ChangePlan, footer ...string) {
	if kind := plan.GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	fmt.Fprintf(p.out, "%s:\n\n", header)

	doomed, kept, keptRows := partition(plan.GetGroups())
	p.writeGroups(doomed)
	if len(footer) > 0 {
		if len(doomed) > 0 {
			fmt.Fprintln(p.out)
		}
		for _, line := range footer {
			fmt.Fprintln(p.out, p.footerLine(line))
		}
	}
	if len(kept) > 0 || len(keptRows) > 0 {
		fmt.Fprintf(p.out, "\nLeft in place:\n\n")
		p.writeGroups(kept)
		for _, row := range keptRows {
			fmt.Fprintf(p.out, "  %s%s\n", row.GetName(), p.trail(gutter, row.GetReason(), row.GetSlow()))
		}
	}
	if tally := Tally(plan); tally != "" {
		fmt.Fprintf(p.out, "\n%s\n", tally)
	}
}

func partition(groups []*contractv1.ChangeGroup) (doomed, kept []*contractv1.ChangeGroup, keptRows []*contractv1.Change) {
	doomed = make([]*contractv1.ChangeGroup, 0, len(groups))
	for _, group := range groups {
		going := &contractv1.ChangeGroup{
			Kind:    group.GetKind(),
			Name:    group.GetName(),
			Feature: group.GetFeature(),
			Action:  group.GetAction(),
			Reason:  group.GetReason(),
			Slow:    group.GetSlow(),
		}
		for _, change := range group.GetChanges() {
			if change.GetAction() == contractv1.Change_ACTION_KEEP {
				keptRows = append(keptRows, change)
				continue
			}
			going.Changes = append(going.Changes, change)
		}
		switch {
		case group.GetAction() != contractv1.Change_ACTION_KEEP:
			doomed = append(doomed, going)
		case len(group.GetChanges()) == 0:
			kept = append(kept, going)
		}
	}
	return doomed, kept, keptRows
}

func (p *Printer) Render(header string, plan *contractv1.ChangePlan) {
	if kind := plan.GetEdgeKind(); kind != "" {
		header += fmt.Sprintf(" (%s edge)", kind)
	}
	fmt.Fprintf(p.out, "%s:\n\n", header)
	p.writeGroups(plan.GetGroups())
	if tally := Tally(plan); tally != "" {
		fmt.Fprintf(p.out, "\n%s\n", tally)
	}
}

func (p *Printer) writeGroups(groups []*contractv1.ChangeGroup) {
	for i, group := range groups {
		if i > 0 && (rendersChanges(groups[i-1]) || rendersChanges(group)) {
			fmt.Fprintln(p.out)
		}
		fmt.Fprintln(p.out, p.GroupLine(group))
		if group.GetAction() == contractv1.Change_ACTION_KEEP {
			continue
		}
		for _, line := range p.changeLines(group.GetChanges()) {
			fmt.Fprintln(p.out, line)
		}
	}
}

func rendersChanges(group *contractv1.ChangeGroup) bool {
	return group.GetAction() != contractv1.Change_ACTION_KEEP && len(group.GetChanges()) > 0
}

func (p *Printer) GroupLine(group *contractv1.ChangeGroup) string {
	var b strings.Builder
	b.WriteString(p.paint(group.GetAction()) + " ")
	if words := actionWords(group.GetAction()); words != "" {
		b.WriteString(words + " ")
	}
	named := bootstrapKinds[group.GetKind()]
	if kind := group.GetKind(); kind != "" && !named {
		b.WriteString(kind + " ")
	}
	b.WriteString(p.style(color.Bold).Sprint(group.GetName()))
	if tag := groupTag(group); named && tag != "" {
		b.WriteString(gutter + p.faint("["+tag+"]"))
	}
	b.WriteString(p.trail(gutter, group.GetReason(), group.GetSlow()))
	return b.String()
}

func (p *Printer) changeLines(changes []*contractv1.Change) []string {
	width := 0
	for _, change := range changes {
		width = max(width, utf8.RuneCountInString(changeLabel(change)))
	}
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		label := changeLabel(change)
		if kind := change.GetKind(); kind != "" {
			label += strings.Repeat(" ", width-utf8.RuneCountInString(label)) + gutter + p.faint(kind)
		}
		lines = append(lines, fmt.Sprintf("    %s %s%s",
			p.paint(change.GetAction()), label, p.trail(typeGutter, change.GetReason(), change.GetSlow())))
	}
	return lines
}

func changeLabel(change *contractv1.Change) string {
	if words := actionWords(change.GetAction()); words != "" {
		return words + " " + change.GetName()
	}
	return change.GetName()
}

var bootstrapKinds = map[string]bool{stackKind: true, edgeKind: true, parameterKind: true}

func groupTag(group *contractv1.ChangeGroup) string {
	if feature := group.GetFeature(); feature != "" {
		return feature
	}
	if group.GetKind() != stackKind {
		return ""
	}
	return baselineTag
}

func (p *Printer) trail(lead, reason string, slow bool) string {
	var b strings.Builder
	if reason != "" {
		b.WriteString(p.faint(lead + "— " + reason))
	}
	if slow {
		b.WriteString(p.faint(slowNote))
	}
	return b.String()
}

func (p *Printer) footerLine(line string) string {
	glyph, rest, found := strings.Cut(line, " ")
	if !found {
		return line
	}
	attrs, ok := sigilAttrs[glyph]
	if !ok {
		return line
	}
	return p.style(attrs...).Sprint(glyph) + " " + rest
}

var sigilAttrs = map[string][]color.Attribute{
	"+": {color.FgGreen},
	"~": {color.FgYellow},
	"±": {color.FgYellow},
	"–": {color.FgRed},
}

func (p *Printer) paint(action contractv1.Change_Action) string {
	glyph := sigil(action)
	attrs, ok := sigilAttrs[glyph]
	if !ok {
		return glyph
	}
	return p.style(attrs...).Sprint(glyph)
}

func (p *Printer) faint(s string) string { return p.style(color.Faint).Sprint(s) }

func (p *Printer) style(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if p.color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func sigil(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_CREATE:
		return "+"
	case contractv1.Change_ACTION_UPDATE:
		return "~"
	case contractv1.Change_ACTION_REPLACE:
		return "±"
	case contractv1.Change_ACTION_DELETE, contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "–"
	case contractv1.Change_ACTION_KEEP:
		return " "
	default:
		return "?"
	}
}

func actionWords(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "disable, then delete"
	case contractv1.Change_ACTION_CREATE,
		contractv1.Change_ACTION_UPDATE,
		contractv1.Change_ACTION_REPLACE,
		contractv1.Change_ACTION_DELETE,
		contractv1.Change_ACTION_KEEP:
		return ""
	default:
		return fmt.Sprintf("act on (%s, an action this CLI does not know)", action)
	}
}

func AllKeep(plan *contractv1.ChangePlan) bool {
	groups := plan.GetGroups()
	if len(groups) == 0 {
		return false
	}
	for _, group := range groups {
		if group.GetAction() != contractv1.Change_ACTION_KEEP {
			return false
		}
	}
	return true
}

func ConfirmVerb(plan *contractv1.ChangePlan) string {
	counts := count(plan)
	if counts[contractv1.Change_ACTION_CREATE] > 0 && len(counts) == 1 {
		return "Create these"
	}
	return "Apply these changes"
}

func Tally(plan *contractv1.ChangePlan) string {
	counts := count(plan)
	var parts []string
	for _, action := range []contractv1.Change_Action{
		contractv1.Change_ACTION_CREATE,
		contractv1.Change_ACTION_UPDATE,
		contractv1.Change_ACTION_REPLACE,
		contractv1.Change_ACTION_DELETE,
	} {
		if n := counts[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", n, tallyVerb(action)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}

func tallyVerb(action contractv1.Change_Action) string {
	switch action {
	case contractv1.Change_ACTION_CREATE:
		return "create"
	case contractv1.Change_ACTION_UPDATE:
		return "update"
	case contractv1.Change_ACTION_REPLACE:
		return "replace"
	default:
		return "delete"
	}
}

func count(plan *contractv1.ChangePlan) map[contractv1.Change_Action]int {
	counts := map[contractv1.Change_Action]int{}
	for _, group := range plan.GetGroups() {
		if group.GetAction() == contractv1.Change_ACTION_KEEP {
			continue
		}
		if len(group.GetChanges()) == 0 {
			tally(counts, group.GetAction())
			continue
		}
		for _, change := range group.GetChanges() {
			tally(counts, change.GetAction())
		}
	}
	return counts
}

func tally(counts map[contractv1.Change_Action]int, action contractv1.Change_Action) {
	switch action {
	case contractv1.Change_ACTION_CREATE,
		contractv1.Change_ACTION_UPDATE,
		contractv1.Change_ACTION_REPLACE,
		contractv1.Change_ACTION_DELETE:
		counts[action]++
	case contractv1.Change_ACTION_DISABLE_THEN_DELETE:
		counts[contractv1.Change_ACTION_DELETE]++
	}
}
