package runui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"
	"google.golang.org/protobuf/reflect/protoreflect"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	sharedStackKind = "stack"
	parameterKind   = "parameters"
	edgeKind        = edge.EdgeGroupKind
	baselineTag     = "core"
	planGutter      = "  "
	planTypeGutter  = "   "
	slowNote        = " (this one is slow)"
)

func (p *projector) plan(m protoreflect.Message) []string {
	return p.planLines(m.Interface().(*planv1.ChangePlan))
}

func (p *projector) planLines(plan *planv1.ChangePlan) []string {
	out := []string{"", planHeadline(plan) + ":"}
	shown, counts := readPlan(plan)
	for i, group := range shown {
		if i == 0 || len(shown[i-1].GetChanges()) > 0 || len(group.GetChanges()) > 0 {
			out = append(out, "")
		}
		out = append(out, p.groupLine(group))
		out = append(out, p.changeLines(group.GetChanges())...)
	}
	if notes := plan.GetNotes(); len(notes) > 0 {
		out = append(out, "")
		for _, note := range notes {
			out = append(out, p.noteLine(note))
		}
	}
	if tally := counts.tally(); tally != "" {
		out = append(out, "", tally)
	}
	return append(out, "")
}

func planHeadline(plan *planv1.ChangePlan) string {
	headline := plan.GetHeadline()
	if headline == "" {
		headline = "Change plan"
	}
	if kind := plan.GetEdgeKind(); kind != "" {
		headline += fmt.Sprintf(", fronted by the %s edge", kind)
	}
	return headline
}

type planCounts struct {
	acted map[planv1.Change_Action]int
	kept  int
}

func readPlan(plan *planv1.ChangePlan) ([]*planv1.ChangeGroup, planCounts) {
	counts := planCounts{acted: map[planv1.Change_Action]int{}}
	shown := make([]*planv1.ChangeGroup, 0, len(plan.GetGroups()))
	for _, group := range plan.GetGroups() {
		acting := actingChanges(group.GetChanges())
		if len(acting) == 0 && group.GetAction() == planv1.Change_ACTION_KEEP {
			counts.kept++
			continue
		}
		counts.kept += len(group.GetChanges()) - len(acting)
		shown = append(shown, acted(group, acting))
		if len(acting) == 0 {
			counts.count(group.GetAction())
			continue
		}
		for _, change := range acting {
			counts.count(change.GetAction())
		}
	}
	return shown, counts
}

func actingChanges(changes []*planv1.Change) []*planv1.Change {
	acting := make([]*planv1.Change, 0, len(changes))
	for _, change := range changes {
		if change.GetAction() != planv1.Change_ACTION_KEEP {
			acting = append(acting, change)
		}
	}
	return acting
}

func acted(group *planv1.ChangeGroup, acting []*planv1.Change) *planv1.ChangeGroup {
	shown := &planv1.ChangeGroup{
		Kind:    group.GetKind(),
		Name:    group.GetName(),
		Feature: group.GetFeature(),
		Action:  group.GetAction(),
		Reason:  group.GetReason(),
		Slow:    group.GetSlow(),
		Changes: acting,
	}
	switch group.GetAction() {
	case planv1.Change_ACTION_KEEP:
		shown.Action, shown.Reason = actingAction(acting), ""
	case planv1.Change_ACTION_UNSPECIFIED:
		shown.Action = actingAction(acting)
	}
	return shown
}

func actingAction(acting []*planv1.Change) planv1.Change_Action {
	creates, deletes := 0, 0
	for _, change := range acting {
		switch change.GetAction() {
		case planv1.Change_ACTION_CREATE:
			creates++
		case planv1.Change_ACTION_DELETE, planv1.Change_ACTION_DISABLE_THEN_DELETE:
			deletes++
		}
	}
	switch {
	case creates == len(acting) && creates > 0:
		return planv1.Change_ACTION_CREATE
	case deletes == len(acting) && deletes > 0:
		return planv1.Change_ACTION_DELETE
	default:
		return planv1.Change_ACTION_UPDATE
	}
}

func (c planCounts) tally() string {
	var parts []string
	for _, action := range []planv1.Change_Action{
		planv1.Change_ACTION_CREATE,
		planv1.Change_ACTION_UPDATE,
		planv1.Change_ACTION_REPLACE,
		planv1.Change_ACTION_DELETE,
	} {
		if n := c.acted[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", n, tallyVerb(action)))
		}
	}
	if c.kept > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", c.kept))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}

func (c planCounts) count(action planv1.Change_Action) {
	switch action {
	case planv1.Change_ACTION_CREATE,
		planv1.Change_ACTION_UPDATE,
		planv1.Change_ACTION_REPLACE,
		planv1.Change_ACTION_DELETE:
		c.acted[action]++
	case planv1.Change_ACTION_DISABLE_THEN_DELETE:
		c.acted[planv1.Change_ACTION_DELETE]++
	case planv1.Change_ACTION_KEEP, planv1.Change_ACTION_UNSPECIFIED:
	}
}

func tallyVerb(action planv1.Change_Action) string {
	switch action {
	case planv1.Change_ACTION_CREATE:
		return "create"
	case planv1.Change_ACTION_UPDATE:
		return "update"
	case planv1.Change_ACTION_REPLACE:
		return "replace"
	default:
		return "delete"
	}
}

func (p *projector) groupLine(group *planv1.ChangeGroup) string {
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
		b.WriteString(planGutter + p.faint("["+tag+"]"))
	}
	b.WriteString(p.trail(planGutter, group.GetReason(), group.GetSlow()))
	return b.String()
}

func (p *projector) changeLines(changes []*planv1.Change) []string {
	width := 0
	for _, change := range changes {
		width = max(width, utf8.RuneCountInString(changeLabel(change)))
	}
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		label := changeLabel(change)
		if kind := change.GetKind(); kind != "" {
			label += strings.Repeat(" ", width-utf8.RuneCountInString(label)) + planGutter + p.faint(kind)
		}
		lines = append(lines, fmt.Sprintf("    %s %s%s",
			p.paint(change.GetAction()), label, p.trail(planTypeGutter, change.GetReason(), change.GetSlow())))
	}
	return lines
}

func changeLabel(change *planv1.Change) string {
	if words := actionWords(change.GetAction()); words != "" {
		return words + " " + change.GetName()
	}
	return change.GetName()
}

var bootstrapKinds = map[string]bool{sharedStackKind: true, edgeKind: true, parameterKind: true}

func groupTag(group *planv1.ChangeGroup) string {
	if feature := group.GetFeature(); feature != "" {
		return feature
	}
	if group.GetKind() != sharedStackKind {
		return ""
	}
	return baselineTag
}

func (p *projector) trail(lead, reason string, slow bool) string {
	var b strings.Builder
	if reason != "" {
		b.WriteString(p.faint(lead + "— " + reason))
	}
	if slow {
		b.WriteString(p.faint(slowNote))
	}
	return b.String()
}

func (p *projector) noteLine(line string) string {
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

func (p *projector) paint(action planv1.Change_Action) string {
	glyph := sigil(action)
	attrs, ok := sigilAttrs[glyph]
	if !ok {
		return glyph
	}
	return p.style(attrs...).Sprint(glyph)
}

func (p *projector) faint(s string) string { return p.style(color.Faint).Sprint(s) }

func (p *projector) style(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if p.present.Color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}

func sigil(action planv1.Change_Action) string {
	switch action {
	case planv1.Change_ACTION_CREATE:
		return "+"
	case planv1.Change_ACTION_UPDATE:
		return "~"
	case planv1.Change_ACTION_REPLACE:
		return "±"
	case planv1.Change_ACTION_DELETE, planv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "–"
	case planv1.Change_ACTION_KEEP:
		return " "
	default:
		return "?"
	}
}

func actionWords(action planv1.Change_Action) string {
	switch action {
	case planv1.Change_ACTION_DISABLE_THEN_DELETE:
		return "disable, then delete"
	case planv1.Change_ACTION_CREATE,
		planv1.Change_ACTION_UPDATE,
		planv1.Change_ACTION_REPLACE,
		planv1.Change_ACTION_DELETE,
		planv1.Change_ACTION_KEEP:
		return ""
	default:
		return fmt.Sprintf("act on (%s, an action this CLI does not know)", action)
	}
}

func mutates(plan *planv1.ChangePlan) bool {
	_, counts := readPlan(plan)
	return len(counts.acted) > 0
}

func ConfirmVerb(plan *planv1.ChangePlan) string {
	_, counts := readPlan(plan)
	if counts.acted[planv1.Change_ACTION_CREATE] > 0 && len(counts.acted) == 1 {
		return "Create these"
	}
	return "Apply these changes"
}
