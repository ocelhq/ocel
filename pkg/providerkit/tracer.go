package providerkit

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type StageID [naming.StageIDLen]byte

func newStageID() StageID {
	var id StageID
	if _, err := rand.Read(id[:]); err != nil {
		panic("mint stage id: " + err.Error())
	}
	if id == (StageID{}) {
		return newStageID()
	}
	return id
}

func derivedStageID(raw []byte) StageID {
	var id StageID
	copy(id[:], raw)
	return id
}

type Stage struct {
	ID       StageID
	ParentID StageID
	Name     string
	Title    string
	Phase    progressv1.Phase
	Phases   []progressv1.Phase
}

func (s Stage) phaseStages() []Stage {
	out := make([]Stage, 0, len(s.Phases))
	for _, phase := range s.Phases {
		out = append(out, PhaseStage(s.Name, phase))
	}
	return out
}

var phaseNames = map[progressv1.Phase]string{
	progressv1.Phase_PHASE_BUILDING:     naming.PhaseBuilding,
	progressv1.Phase_PHASE_UPLOADING:    naming.PhaseUploading,
	progressv1.Phase_PHASE_PROVISIONING: naming.PhaseProvisioning,
	progressv1.Phase_PHASE_FINALIZING:   naming.PhaseFinalizing,
	progressv1.Phase_PHASE_DELETING:     naming.PhaseDeleting,
}

var phaseTitles = map[progressv1.Phase]string{
	progressv1.Phase_PHASE_BUILDING:     "Building",
	progressv1.Phase_PHASE_UPLOADING:    "Uploading",
	progressv1.Phase_PHASE_PROVISIONING: "Provisioning",
	progressv1.Phase_PHASE_FINALIZING:   "Finalizing",
	progressv1.Phase_PHASE_DELETING:     "Deleting",
}

const maxStageTitleLen = 200

func stripControlChars(s string, capLen int) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if capLen > 0 && b.Len() >= capLen {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func sanitizeTitle(title string) string {
	out := stripControlChars(title, maxStageTitleLen)
	if out == "" {
		return "stage"
	}
	return out
}

func sanitizeMessage(msg string) string {
	return stripControlChars(msg, 0)
}

const (
	environmentUnitTitle = "Environment"
	edgeUnitTitle        = "Edge"
	hostnamesUnitTitle   = "Hostnames"
	promotionUnitTitle   = "Promotion"
	infraUnitTitle       = "Shared infrastructure"
	connectorUnitTitle   = "Connector"
)

func UnitStage(name, title string, phases ...progressv1.Phase) Stage {
	return Stage{ID: derivedStageID(naming.UnitID(name)), Name: name, Title: sanitizeTitle(title), Phases: phases}
}

func PhaseStage(unitName string, phase progressv1.Phase) Stage {
	name := phaseNames[phase]
	return Stage{
		ID:       derivedStageID(naming.PhaseID(unitName, name)),
		ParentID: derivedStageID(naming.UnitID(unitName)),
		Name:     name,
		Title:    phaseTitles[phase],
		Phase:    phase,
	}
}

func NewStage(parent Stage, title string) Stage {
	return Stage{ID: newStageID(), ParentID: parent.ID, Title: sanitizeTitle(title)}
}

var attributeKeys = map[string]progressv1.AttributeKey{
	provider.AttrKeyApp:           progressv1.AttributeKey_ATTRIBUTE_KEY_APP,
	provider.AttrKeyResourceCount: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT,
	provider.AttrKeyBytes:         progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES,
	provider.AttrKeyDurationMS:    progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS,
	provider.AttrKeyResourceType:  progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE,
	provider.AttrKeyResourceName:  progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME,
	provider.AttrKeyErrorKind:     progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND,
}

func AttributeKey(key string) progressv1.AttributeKey { return attributeKeys[key] }

type stageScope struct {
	sender *eventStream
	trace  *eventTrace

	mu       sync.Mutex
	declared map[StageID]bool
}

func newStageScope(sender *eventStream) *stageScope {
	return &stageScope{sender: sender, trace: newEventTrace(sender), declared: map[StageID]bool{}}
}

func (s *stageScope) declare(stages ...Stage) {
	s.mu.Lock()
	var fresh []Stage
	for _, stage := range stages {
		for _, declaring := range append([]Stage{stage}, stage.phaseStages()...) {
			if s.declared[declaring.ID] {
				continue
			}
			s.declared[declaring.ID] = true
			fresh = append(fresh, declaring)
		}
	}
	s.mu.Unlock()
	s.trace.DeclareStages(fresh...)
}

func (s *stageScope) unit(stage Stage, do func(*unitRun) error) error {
	s.declare(stage)
	start := time.Now()
	err := do(&unitRun{scope: s, stage: stage})
	s.trace.Span(stage.ID, stage.ParentID, stage.Title, start, time.Now(), err)
	return err
}

type unitRun struct {
	scope *stageScope
	stage Stage
}

func (u *unitRun) phase(phase progressv1.Phase, do func(edge.Progress) error) error {
	working := PhaseStage(u.stage.Name, phase)
	u.scope.declare(working)
	start := time.Now()
	err := do(newProgress(u.scope.sender, working))
	u.scope.trace.Span(working.ID, working.ParentID, working.Title, start, time.Now(), err)
	return err
}

type eventTrace struct {
	sender *eventStream
}

func newEventTrace(sender *eventStream) *eventTrace {
	return &eventTrace{sender: sender}
}

func (t *eventTrace) DeclareStages(stages ...Stage) {
	if len(stages) == 0 {
		return
	}
	pb := make([]*progressv1.Stage, len(stages))
	for i, s := range stages {
		pb[i] = &progressv1.Stage{
			Id:       s.ID[:],
			ParentId: nonZeroStageID(s.ParentID),
			Title:    s.Title,
			Phase:    s.Phase,
		}
	}
	t.sender.send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_StagePlan{StagePlan: &progressv1.StagePlanEvent{Stages: pb}},
	})
}

func (t *eventTrace) Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...edge.Attr) {
	status := progressv1.SpanStatus_SPAN_STATUS_OK
	if err != nil {
		status = progressv1.SpanStatus_SPAN_STATUS_ERROR
		attrs = append(attrs, edge.Attr{Key: provider.AttrKeyErrorKind, Value: provider.ClassifyError(err)})
	}

	pbAttrs := make([]*progressv1.SpanAttribute, len(attrs))
	for i, a := range attrs {
		pbAttrs[i] = &progressv1.SpanAttribute{Key: attributeKeys[a.Key], Value: a.Value}
	}

	t.sender.send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Span{Span: &progressv1.SpanEvent{
			SpanId:            id[:],
			ParentSpanId:      nonZeroStageID(parentID),
			Name:              name,
			StartTimeUnixNano: start.UnixNano(),
			EndTimeUnixNano:   end.UnixNano(),
			Status:            status,
			Attributes:        pbAttrs,
		}},
	})
}

func nonZeroStageID(id StageID) []byte {
	if id == (StageID{}) {
		return nil
	}
	return id[:]
}
