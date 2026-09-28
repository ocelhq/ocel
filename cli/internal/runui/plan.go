package runui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"

	"github.com/ocelhq/ocel/pkg/edge"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	baselineTag    = "core"
	planGutter     = "  "
	planTypeGutter = "   "
	slowNote       = " (slow)"
)

func planLines(present Presentation, plan *planv1.ChangePlan) []string {
	out := []string{planHeadline(plan) + ":"}
	shown, counts := readPlan(plan)
	for i, group := range shown {
		if i == 0 || len(shown[i-1].GetChanges()) > 0 || len(group.GetChanges()) > 0 {
			out = append(out, "")
		}
		out = append(out, groupLine(present, group))
		out = append(out, changeLines(present, group.GetChanges())...)
	}
	if notes := plan.GetNotes(); len(notes) > 0 {
		out = append(out, "")
		for _, note := range notes {
			out = append(out, noteLine(present, note))
		}
	}
	if tally := counts.tally(); tally != "" {
		out = append(out, "", tally)
	}
	return out
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
	acted   map[planv1.Change_Action]int
	adopted int
	kept    int
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
		shown.Action, shown.Reason = provider.RollUpProto(group.GetChanges()), ""
	case planv1.Change_ACTION_UNSPECIFIED:
		shown.Action = provider.RollUpProto(group.GetChanges())
	}
	return shown
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
			parts = append(parts, fmt.Sprintf("%d to %s", n, faceOf(action).verb))
		}
	}
	if c.adopted > 0 {
		parts = append(parts, fmt.Sprintf("%d adopted", c.adopted))
	}
	if c.kept > 0 {
		parts = append(parts, fmt.Sprintf("%d unchanged", c.kept))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + "."
}

func (c *planCounts) count(action planv1.Change_Action) {
	if action == planv1.Change_ACTION_ADOPT {
		c.adopted++
	}
	if tallied := faceOf(action).tallyAs; tallied != planv1.Change_ACTION_UNSPECIFIED {
		c.acted[tallied]++
	}
}

func groupLine(present Presentation, group *planv1.ChangeGroup) string {
	var b strings.Builder
	b.WriteString(sigil(present, group.GetAction()) + " ")
	if words := faceOf(group.GetAction()).words; words != "" {
		b.WriteString(words + " ")
	}
	named := namedKind(group.GetKind())
	if kind := group.GetKind(); kind != "" && !named {
		b.WriteString(kind + " ")
	}
	b.WriteString(colorFor(present, color.Bold).Sprint(group.GetName()))
	if tag := groupTag(group); named && tag != "" {
		b.WriteString(planGutter + faint(present, "["+tag+"]"))
	}
	b.WriteString(trail(present, planGutter, group.GetReason(), group.GetSlow()))
	return b.String()
}

func changeLines(present Presentation, changes []*planv1.Change) []string {
	width := 0
	for _, change := range changes {
		width = max(width, utf8.RuneCountInString(changeLabel(change)))
	}
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		label := changeLabel(change)
		if kind := change.GetKind(); kind != "" {
			label += strings.Repeat(" ", width-utf8.RuneCountInString(label)) + planGutter + faint(present, kind)
		}
		lines = append(lines, fmt.Sprintf("    %s %s%s",
			sigil(present, change.GetAction()), label, trail(present, planTypeGutter, change.GetReason(), change.GetSlow())))
	}
	return lines
}

func changeLabel(change *planv1.Change) string {
	if words := faceOf(change.GetAction()).words; words != "" {
		return words + " " + change.GetName()
	}
	return change.GetName()
}

var spineKinds = map[string]bool{
	provider.StackGroupKind:     true,
	provider.ParameterGroupKind: true,
	edge.EdgeGroupKind:          true,
}

func namedKind(kind string) bool { return spineKinds[kind] }

func groupTag(group *planv1.ChangeGroup) string {
	if feature := group.GetFeature(); feature != "" {
		return feature
	}
	if group.GetKind() != provider.StackGroupKind {
		return ""
	}
	return baselineTag
}

func trail(present Presentation, lead, reason string, slow bool) string {
	var b strings.Builder
	if reason != "" {
		b.WriteString(faint(present, lead+"— "+reason))
	}
	if slow {
		b.WriteString(faint(present, slowNote))
	}
	return b.String()
}

func noteLine(present Presentation, line string) string {
	glyph, rest, found := strings.Cut(line, " ")
	if !found {
		return line
	}
	attrs, ok := sigilAttrs[glyph]
	if !ok {
		return line
	}
	return colorFor(present, attrs...).Sprint(glyph) + " " + rest
}

var sigilAttrs = map[string][]color.Attribute{
	"+": {color.FgGreen},
	"~": {color.FgYellow},
	"±": {color.FgYellow},
	"–": {color.FgRed},
}

func sigil(present Presentation, action planv1.Change_Action) string {
	glyph := faceOf(action).sigil
	attrs, ok := sigilAttrs[glyph]
	if !ok {
		return glyph
	}
	return colorFor(present, attrs...).Sprint(glyph)
}

func faint(present Presentation, s string) string { return colorFor(present, color.Faint).Sprint(s) }

type actionFace struct {
	sigil   string
	verb    string
	words   string
	tallyAs planv1.Change_Action
}

var actionFaces = map[planv1.Change_Action]actionFace{
	planv1.Change_ACTION_CREATE:              {sigil: "+", verb: "create", tallyAs: planv1.Change_ACTION_CREATE},
	planv1.Change_ACTION_UPDATE:              {sigil: "~", verb: "update", tallyAs: planv1.Change_ACTION_UPDATE},
	planv1.Change_ACTION_REPLACE:             {sigil: "±", verb: "replace", tallyAs: planv1.Change_ACTION_REPLACE},
	planv1.Change_ACTION_DELETE:              {sigil: "–", verb: "delete", tallyAs: planv1.Change_ACTION_DELETE},
	planv1.Change_ACTION_DISABLE_THEN_DELETE: {sigil: "–", verb: "delete", words: "disable, then delete", tallyAs: planv1.Change_ACTION_DELETE},
	planv1.Change_ACTION_KEEP:                {sigil: " "},
	planv1.Change_ACTION_ADOPT:               {sigil: "=", words: "adopt"},
}

func faceOf(action planv1.Change_Action) actionFace {
	if face, known := actionFaces[action]; known {
		return face
	}
	return actionFace{sigil: "?", words: fmt.Sprintf("act on (%s, an action this CLI does not know)", action)}
}
