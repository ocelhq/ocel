package pulumi

import (
	"errors"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

var errResourceOperationFailed = errors.New("resource operation failed")

const (
	engineDrainGrace         = 30 * time.Second
	maxResourceIdentifierLen = 256
	maxDiagnosticLen         = 2048
	diagnosticSeverityError  = "error"
)

type resourceOp struct {
	Action provider.ChangeAction
	Type   string
	Name   string
	Start  time.Time
	End    time.Time
	Failed bool

	Diagnostic string
}

func plainText(message string, secrets []string) string {
	text := maskSecrets(colors.Never.Colorize(message), secrets)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxDiagnosticLen {
		text = strings.ToValidUTF8(text[:maxDiagnosticLen], "") + "…"
	}
	return text
}

var performedActions = map[apitype.OpType]provider.ChangeAction{
	apitype.OpCreate:            provider.ActionCreate,
	apitype.OpImport:            provider.ActionCreate,
	apitype.OpUpdate:            provider.ActionUpdate,
	apitype.OpReplace:           provider.ActionReplace,
	apitype.OpImportReplacement: provider.ActionReplace,
	apitype.OpDelete:            provider.ActionDelete,
}

func performedAction(m apitype.StepEventMetadata, failed bool) (provider.ChangeAction, bool) {
	if m.Type == stackResourceType {
		return "", false
	}
	if failed && replacementSteps[m.Op] {
		return provider.ActionReplace, true
	}
	action, changed := performedActions[m.Op]
	return action, changed
}

var replacementSteps = map[apitype.OpType]bool{
	apitype.OpCreateReplacement: true,
	apitype.OpDeleteReplaced:    true,
	apitype.OpDiscardReplaced:   true,
}

func capIdentifier(s string) string {
	if len(s) <= maxResourceIdentifierLen {
		return s
	}
	return strings.ToValidUTF8(s[:maxResourceIdentifierLen], "")
}

const (
	urnPrefix        = "urn:pulumi:"
	urnPartDelimiter = "::"
)

func resourceNameFromURN(raw string) string {
	if !strings.HasPrefix(raw, urnPrefix) {
		return ""
	}
	parts := strings.Split(raw, urnPartDelimiter)
	if len(parts) < 4 {
		return ""
	}
	return capIdentifier(strings.Join(parts[3:], urnPartDelimiter))
}

func drainResourceOps(engineEvents <-chan events.EngineEvent, steps *resourceSteps) <-chan []resourceOp {
	result := make(chan []resourceOp, 1)
	go func() {
		for event := range engineEvents {
			steps.consume(event, time.Now())
		}
		steps.close(time.Now())
		result <- steps.ops
	}()
	return result
}

func awaitResourceOps(result <-chan []resourceOp, grace time.Duration) []resourceOp {
	select {
	case ops := <-result:
		return ops
	case <-time.After(grace):
		return nil
	}
}

var recordedByProvider = progress.Attr{Key: progress.AttrKeyRecordedByProvider, Value: "true"}

func reportResourceOps(runProgress progress.Log, ops []resourceOp) {
	if runProgress == nil {
		return
	}
	for _, s := range ops {
		var opErr error
		if s.Failed {
			opErr = errResourceOperationFailed
		}
		attrs := []progress.Attr{recordedByProvider, provider.AttrDurationMS(s.End.Sub(s.Start))}
		if s.Type != "" {
			attrs = append(attrs, provider.AttrResourceType(s.Type))
		}
		if s.Name != "" {
			attrs = append(attrs, provider.AttrResourceName(s.Name))
		}
		if s.Action != "" {
			attrs = append(attrs, provider.AttrResourceAction(s.Action))
		}
		runProgress.Span(s.progressName(), s.Start, s.End, opErr, attrs...)
		if s.Failed && s.Diagnostic != "" {
			runProgress.Error(s.label() + ": " + s.Diagnostic)
		}
	}
}

func (s resourceOp) label() string {
	switch {
	case s.Name != "" && s.Type != "":
		return s.Name + " (" + s.Type + ")"
	case s.Name != "":
		return s.Name
	case s.Type != "":
		return s.Type
	default:
		return "a resource"
	}
}

func (s resourceOp) progressName() string {
	if s.Failed {
		return errResourceOperationFailed.Error()
	}
	return actionName(s.Action)
}

func actionName(action provider.ChangeAction) string {
	return string(action) + " resource"
}
