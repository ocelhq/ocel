package runui

import (
	"encoding/hex"
	"strings"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func stageKey(id []byte) string { return hex.EncodeToString(id) }

func Envelopes(ev *progressv1.OperationEvent) []Envelope {
	switch e := ev.GetEvent().(type) {
	case *progressv1.OperationEvent_StagePlan:
		decls := make([]StageDecl, 0, len(e.StagePlan.GetStages()))
		for _, stage := range e.StagePlan.GetStages() {
			decls = append(decls, StageDecl{
				ID:     stageKey(stage.GetId()),
				Parent: stageKey(stage.GetParentId()),
				Title:  stage.GetTitle(),
			})
		}
		if len(decls) == 0 {
			return nil
		}
		return []Envelope{{Stages: decls}}

	case *progressv1.OperationEvent_Progress:
		p := e.Progress
		return []Envelope{{Progress: &Progress{
			StageID: stageKey(p.GetStageId()),
			Message: p.GetMessage(),
			Current: p.GetCurrent(),
			Total:   p.GetTotal(),
			HasBar:  p.Total != nil && p.GetTotal() > 0,
		}}}

	case *progressv1.OperationEvent_Log:
		out := make([]Envelope, 0, 1)
		for line := range strings.SplitSeq(e.Log.GetMessage(), "\n") {
			out = append(out, Envelope{Log: &Log{StageID: stageKey(e.Log.GetStageId()), Line: line}})
		}
		return out

	case *progressv1.OperationEvent_Span:
		s := e.Span
		return []Envelope{{End: &StageEnd{
			StageID: stageKey(s.GetSpanId()),
			Failed:  s.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR,
		}}}

	case *progressv1.OperationEvent_Plan:
		return []Envelope{{Plan: planOf(e.Plan)}}

	case *progressv1.OperationEvent_Result:
		return []Envelope{{Result: resultOf(e.Result)}}

	case *progressv1.OperationEvent_Degraded:
		return []Envelope{{Diagnostic: []string{e.Degraded.GetNeed() + ": " + e.Degraded.GetDetail()}}}

	case *progressv1.OperationEvent_DnsOwed:
		lines := []string{e.DnsOwed.GetHeadline()}
		for _, rec := range e.DnsOwed.GetRecords() {
			lines = append(lines, "  "+rec.GetType()+" "+rec.GetName()+" -> "+rec.GetValue())
		}
		return []Envelope{{Diagnostic: append(lines, e.DnsOwed.GetNotes()...)}}
	}
	return nil
}

func planOf(pb *planv1.ChangePlan) *Plan {
	plan := &Plan{Subject: pb.GetSubject(), EdgeKind: pb.GetEdgeKind()}
	for _, group := range pb.GetGroups() {
		g := Group{Kind: group.GetKind(), Name: group.GetName(), Feature: group.GetFeature()}
		for _, change := range group.GetChanges() {
			g.Rows = append(g.Rows, Row{
				Kind:   change.GetKind(),
				Name:   change.GetName(),
				Action: actionOf(change.GetAction()),
				Reason: change.GetReason(),
				Slow:   change.GetSlow(),
			})
		}
		if len(g.Rows) == 0 {
			g.Rows = append(g.Rows, Row{
				Kind:   group.GetKind(),
				Name:   group.GetName(),
				Action: actionOf(group.GetAction()),
				Reason: group.GetReason(),
				Slow:   group.GetSlow(),
			})
		}
		plan.Groups = append(plan.Groups, g)
	}
	return plan
}

func actionOf(action planv1.Change_Action) Action {
	switch action {
	case planv1.Change_ACTION_CREATE:
		return Create
	case planv1.Change_ACTION_UPDATE:
		return Update
	case planv1.Change_ACTION_REPLACE:
		return Replace
	case planv1.Change_ACTION_DELETE:
		return Delete
	case planv1.Change_ACTION_DISABLE_THEN_DELETE:
		return DisableThenDelete
	default:
		return Keep
	}
}

func resultOf(pb *progressv1.ResultEvent) *Result {
	res := &Result{
		Success: pb.GetSuccess(),
		Error:   pb.GetError(),
		AppURLs: pb.GetAppUrls(),
	}
	res.Headline = "Deployed"
	if !pb.GetSuccess() {
		res.Headline = "Deploy failed"
	}
	if note := pb.GetUrlNote(); note != "" {
		res.Diagnostic = append(res.Diagnostic, note)
	}
	return res
}
