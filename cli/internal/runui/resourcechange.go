package runui

import (
	"fmt"
	"strings"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var resourceChangeOrder = []planv1.Change_Action{
	planv1.Change_ACTION_CREATE,
	planv1.Change_ACTION_UPDATE,
	planv1.Change_ACTION_REPLACE,
	planv1.Change_ACTION_DELETE,
}

var resourceChangesDone = map[planv1.Change_Action]string{
	planv1.Change_ACTION_CREATE:  "created",
	planv1.Change_ACTION_UPDATE:  "updated",
	planv1.Change_ACTION_REPLACE: "replaced",
	planv1.Change_ACTION_DELETE:  "deleted",
}

type resourceChange struct {
	face   actionFace
	label  string
	failed bool
}

type resourceTally struct {
	done   map[planv1.Change_Action]int
	failed int
}

func resourceChangeOf(ev *streamv1.RunEvent) (resourceChange, bool) {
	attrs := map[progressv1.AttributeKey]string{}
	for _, a := range ev.GetEnded().GetAttributes() {
		attrs[a.GetKey()] = a.GetValue()
	}
	action := provider.ActionProto(provider.ChangeAction(attrs[progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_ACTION]))
	face := faceOf(action)
	name, typ := attrs[progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME], attrs[progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE]
	failed := ev.GetEnded().GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	if _, changes := resourceChangesDone[face.tallyAs]; !changes {
		if !failed || name+typ == "" {
			return resourceChange{}, false
		}
		face = actionFace{}
	}
	return resourceChange{face: face, label: resourceLabel(name, typ), failed: failed}, true
}

func resourceLabel(name, typ string) string {
	switch {
	case name != "" && typ != "":
		return name + " (" + typ + ")"
	case name != "":
		return name
	case typ != "":
		return typ
	default:
		return "a resource"
	}
}

func (c resourceChange) render(present Presentation) string {
	if c.failed && c.face.verb == "" {
		return unitMarks[progressv1.SpanStatus_SPAN_STATUS_ERROR].render(present) + " " + c.label + " failed"
	}
	if c.failed {
		return unitMarks[progressv1.SpanStatus_SPAN_STATUS_ERROR].render(present) + " " + c.label + " failed to " + c.face.verb
	}
	return label{c.face.sigil, sigilAttrs[c.face.sigil]}.render(present) + " " + muted(present, c.label+" "+resourceChangesDone[c.face.tallyAs])
}

func (t *resourceTally) count(c resourceChange) {
	if c.failed {
		t.failed++
		return
	}
	if t.done == nil {
		t.done = map[planv1.Change_Action]int{}
	}
	t.done[c.face.tallyAs]++
}

func (t resourceTally) summary() string {
	var parts []string
	total := t.failed
	for _, action := range resourceChangeOrder {
		if n := t.done[action]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, resourceChangesDone[action]))
			total += n
		}
	}
	if t.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", t.failed))
	}
	noun := "resources"
	if total == 1 {
		noun = "resource"
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		_, done, _ := strings.Cut(parts[0], " ")
		return fmt.Sprintf(" — %d %s %s", total, noun, done)
	}
	return fmt.Sprintf(" — %d %s: %s", total, noun, strings.Join(parts, ", "))
}
