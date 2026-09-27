package providerserver

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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
	Subject  string
}

func (s Stage) scoped(ev *progressv1.OperationEvent) *progressv1.OperationEvent {
	ev.Phase = s.Phase
	ev.Subject = s.Subject
	ev.SpanId = s.ID[:]
	return ev
}

func (s Stage) phaseStages() []Stage {
	out := make([]Stage, 0, len(s.Phases))
	for _, phase := range s.Phases {
		stage := PhaseStage(s.Name, phase)
		stage.Subject = s.Subject
		out = append(out, stage)
	}
	return out
}

var phaseNames = map[progressv1.Phase]string{
	progressv1.Phase_PHASE_BUILD:     naming.PhaseBuilding,
	progressv1.Phase_PHASE_DEPLOY:    naming.PhaseUploading,
	progressv1.Phase_PHASE_PROVISION: naming.PhaseProvisioning,
	progressv1.Phase_PHASE_PROMOTE:   naming.PhaseFinalizing,
	progressv1.Phase_PHASE_DESTROY:   naming.PhaseDeleting,
}

var phaseTitles = map[progressv1.Phase]string{
	progressv1.Phase_PHASE_BUILD:     "Building",
	progressv1.Phase_PHASE_DEPLOY:    "Uploading",
	progressv1.Phase_PHASE_PROVISION: "Provisioning",
	progressv1.Phase_PHASE_PROMOTE:   "Finalizing",
	progressv1.Phase_PHASE_DESTROY:   "Deleting",
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
	return Stage{ID: newStageID(), ParentID: parent.ID, Title: sanitizeTitle(title), Phase: parent.Phase, Subject: parent.Subject}
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
	s.trace.Start(time.Now(), fresh...)
}

func (s *stageScope) unit(stage Stage, do func(*unitRun) error) error {
	s.declare(stage)
	start := time.Now()
	run := &unitRun{scope: s, stage: stage}
	err := do(run)
	s.trace.End(stage, run.last, start, time.Now(), err)
	return err
}

type unitRun struct {
	scope *stageScope
	stage Stage
	last  progressv1.Phase
}

func (u *unitRun) phase(phase progressv1.Phase, do func(edge.Progress) error) error {
	working := PhaseStage(u.stage.Name, phase)
	working.Subject = u.stage.Subject
	u.scope.declare(working)
	u.last = phase
	start := time.Now()
	err := do(newProgress(u.scope.sender, working))
	u.scope.trace.End(working, phase, start, time.Now(), err)
	return err
}

type eventTrace struct {
	sender *eventStream
}

func newEventTrace(sender *eventStream) *eventTrace {
	return &eventTrace{sender: sender}
}

func (t *eventTrace) Start(at time.Time, stages ...Stage) {
	for _, s := range stages {
		t.sender.send(s.scoped(&progressv1.OperationEvent{
			TimeUnixNano: at.UnixNano(),
			Message:      s.Title,
			Event: &progressv1.OperationEvent_Started{Started: &progressv1.Started{
				ParentSpanId: nonZeroStageID(s.ParentID),
			}},
		}))
	}
}

func (t *eventTrace) End(stage Stage, phase progressv1.Phase, start, end time.Time, err error, attrs ...edge.Attr) {
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
		TimeUnixNano: end.UnixNano(),
		Phase:        phase,
		Subject:      stage.Subject,
		SpanId:       stage.ID[:],
		Event: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
			Status:            status,
			StartTimeUnixNano: start.UnixNano(),
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
