package deploy

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type spanCall struct {
	id, parentID StageID
	name         string
	start, end   time.Time
	err          error
	attrs        []Attr
}

type fakeTracer struct {
	mu       sync.Mutex
	declared [][]Stage
	spans    []spanCall
}

func (f *fakeTracer) DeclareStages(stages ...Stage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.declared = append(f.declared, stages)
}

func (f *fakeTracer) Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...Attr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, spanCall{id, parentID, name, start, end, err, attrs})
}

func TestUnitAndPhaseIDsMatchTheSharedNamingDigests(t *testing.T) {
	t.Parallel()

	unit := UnitStage(naming.UnitEnvironment, "Environment")
	if unit.ParentID != (StageID{}) {
		t.Fatalf("unit ParentID = %v, want zero value: a unit is a root", unit.ParentID)
	}
	if got := hex.EncodeToString(unit.ID[:]); got != "9f2ecbbdfa2db89d" {
		t.Errorf("unit id = %s, want the naming digest 9f2ecbbdfa2db89d", got)
	}
	phase := PhaseStage(unit, progressv1.Phase_PHASE_PROVISIONING)
	if phase.ParentID != unit.ID {
		t.Fatalf("phase ParentID = %v, want %v", phase.ParentID, unit.ID)
	}
	if got := hex.EncodeToString(phase.ID[:]); got != "ed0ca2aae3a67905" {
		t.Errorf("phase id = %s, want the naming digest ed0ca2aae3a67905", got)
	}
	if UnitStage(naming.UnitEnvironment, "Environment").ID != unit.ID {
		t.Error("the same canonical name derived two different unit ids")
	}
}

func TestDetailStagesMintDistinctIDsUnderTheirPhase(t *testing.T) {
	t.Parallel()

	phase := PhaseStage(UnitStage(naming.UnitEnvironment, "Environment"), progressv1.Phase_PHASE_PROVISIONING)
	child := NewStage(phase, "web")
	if child.ParentID != phase.ID {
		t.Fatalf("child.ParentID = %v, want %v", child.ParentID, phase.ID)
	}
	if NewStage(phase, "web").ID == child.ID {
		t.Fatal("two minted detail stages got the same id")
	}
}

func TestSpanForStageUsesTheStageIDAsTheSpanID(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	stage := NewStage(PhaseStage(UnitStage(naming.UnitEnvironment, "Environment"), progressv1.Phase_PHASE_PROVISIONING), "web")
	start := time.Unix(100, 0)
	end := time.Unix(101, 0)
	spanForStage(ft, stage, start, end, nil, AttrApp("web"))

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(ft.spans))
	}
	got := ft.spans[0]
	if got.id != stage.ID {
		t.Errorf("span id = %v, want stage id %v", got.id, stage.ID)
	}
	if got.parentID != stage.ParentID {
		t.Errorf("span parentID = %v, want %v", got.parentID, stage.ParentID)
	}
	if got.name != stage.Title {
		t.Errorf("span name = %q, want stage title %q", got.name, stage.Title)
	}
}

func TestSpanUnderMintsAFreshIDDistinctFromItsParent(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := UnitStage(naming.UnitEnvironment, "Environment")
	spanUnder(ft, parent.ID, "pulumi resource operations", time.Unix(0, 0), time.Unix(1, 0), nil)

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(ft.spans))
	}
	if ft.spans[0].id == parent.ID {
		t.Error("spanUnder reused the parent id as its own span id")
	}
	if ft.spans[0].parentID != parent.ID {
		t.Errorf("parentID = %v, want %v", ft.spans[0].parentID, parent.ID)
	}
}

func TestNilTracerIsANoOp(t *testing.T) {
	t.Parallel()

	declareStages(nil, UnitStage(naming.UnitEnvironment, "Preparing"))
	spanForStage(nil, UnitStage(naming.UnitEnvironment, "Preparing"), time.Now(), time.Now(), errors.New("boom"))
	spanUnder(nil, StageID{}, "x", time.Now(), time.Now(), nil)
}

func TestClassifyErrorNeverReturnsFreeText(t *testing.T) {
	t.Parallel()

	secret := "postgres://user:hunter2@10.0.0.1:5432/db?sslmode=disable"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"canceled", context.Canceled, ErrorKindCanceled},
		{"wrapped canceled", errWrap(context.Canceled, secret), ErrorKindCanceled},
		{"deadline", context.DeadlineExceeded, ErrorKindTimeout},
		{"generic with embedded secret", errors.New("connect failed: " + secret), ErrorKindFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyError(tc.err)
			if got != tc.want {
				t.Errorf("ClassifyError() = %q, want %q", got, tc.want)
			}
			if got == secret {
				t.Fatal("ClassifyError leaked the raw error text")
			}
		})
	}
}

func errWrap(base error, msg string) error {
	return &wrapped{base: base, msg: msg}
}

type wrapped struct {
	base error
	msg  string
}

func (w *wrapped) Error() string { return w.msg + ": " + w.base.Error() }
func (w *wrapped) Unwrap() error { return w.base }

func TestNewStageStripsControlCharactersAndCapsTheTitle(t *testing.T) {
	t.Parallel()

	dirty := "web\x1b[31m\x07" + strings.Repeat("x", maxStageTitleLen*2)
	root := UnitStage(naming.UnitEnvironment, dirty)
	if strings.ContainsAny(root.Title, "\x1b\x07") {
		t.Fatalf("Title = %q, still contains control characters", root.Title)
	}
	if len(root.Title) > maxStageTitleLen {
		t.Fatalf("len(Title) = %d, want <= %d", len(root.Title), maxStageTitleLen)
	}

	child := NewStage(root, dirty)
	if strings.ContainsAny(child.Title, "\x1b\x07") {
		t.Fatalf("Title = %q, still contains control characters", child.Title)
	}

	if got := UnitStage(naming.UnitEnvironment, "   ").Title; got != "stage" {
		t.Errorf("UnitStage(all-control/blank) Title = %q, want the fallback", got)
	}
}

func TestSanitizeMessageStripsControlCharactersWithoutTruncatingOrFallingBack(t *testing.T) {
	t.Parallel()

	dirty := "Destroying app stack prod--web\x1b\x07--r1"
	got := sanitizeMessage(dirty)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("sanitizeMessage(%q) = %q, still contains control characters", dirty, got)
	}
	if got != "Destroying app stack prod--web--r1" {
		t.Errorf("sanitizeMessage(%q) = %q, want the control characters dropped and the rest untouched", dirty, got)
	}

	long := strings.Repeat("x", maxStageTitleLen*2)
	if got := sanitizeMessage(long); len(got) != len(long) {
		t.Errorf("sanitizeMessage truncated a long message to %d chars, want it left at %d: unlike a stage title, a progress message has no length cap", len(got), len(long))
	}

	if got := sanitizeMessage("   "); got != "" {
		t.Errorf("sanitizeMessage(blank) = %q, want empty: unlike a stage title, a message has no non-empty fallback", got)
	}
}

func TestAttrHelpersUseTheBoundedAttributeKeys(t *testing.T) {
	t.Parallel()

	if got := AttrApp("web"); got.Key != progressv1.AttributeKey_ATTRIBUTE_KEY_APP || got.Value != "web" {
		t.Errorf("AttrApp() = %+v", got)
	}
	if got := AttrResourceCount(3); got.Key != progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT || got.Value != "3" {
		t.Errorf("AttrResourceCount() = %+v", got)
	}
	if got := AttrDurationMS(1500 * time.Millisecond); got.Key != progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS || got.Value != "1500" {
		t.Errorf("AttrDurationMS() = %+v", got)
	}
	if got := AttrResourceType("aws:s3/bucket:Bucket"); got.Key != progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE || got.Value != "aws:s3/bucket:Bucket" {
		t.Errorf("AttrResourceType() = %+v", got)
	}
	if got := AttrResourceName("my-bucket"); got.Key != progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME || got.Value != "my-bucket" {
		t.Errorf("AttrResourceName() = %+v", got)
	}
}
