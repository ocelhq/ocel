package deploy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
	final    []bool
	spans    []spanCall
}

func (f *fakeTracer) DeclareStages(final bool, stages ...Stage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.declared = append(f.declared, stages)
	f.final = append(f.final, final)
}

func (f *fakeTracer) Span(id, parentID StageID, name string, start, end time.Time, err error, attrs ...Attr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, spanCall{id, parentID, name, start, end, err, attrs})
}

func TestNewStageMintsDistinctIDsParentedCorrectly(t *testing.T) {
	t.Parallel()

	root := NewRootStage("Provisioning")
	if root.ParentID != (StageID{}) {
		t.Fatalf("root ParentID = %v, want zero value", root.ParentID)
	}
	child := NewStage(root, "web")
	if child.ParentID != root.ID {
		t.Fatalf("child.ParentID = %v, want %v", child.ParentID, root.ID)
	}
	other := NewRootStage("Provisioning")
	if root.ID == other.ID {
		t.Fatal("two minted stages got the same id")
	}
}

func TestSpanForStageUsesTheStageIDAsTheSpanID(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	stage := NewStage(NewRootStage("Provisioning"), "web")
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
	parent := NewRootStage("Provisioning")
	spanUnder(ft, parent.ID, engineBatchSpanName, time.Unix(0, 0), time.Unix(1, 0), nil)

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

	declareStages(nil, true, NewRootStage("Preparing"))
	spanForStage(nil, NewRootStage("Preparing"), time.Now(), time.Now(), errors.New("boom"))
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
	root := NewRootStage(dirty)
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

	if got := NewRootStage("   ").Title; got != "stage" {
		t.Errorf("NewRootStage(all-control/blank) Title = %q, want the fallback", got)
	}
}

func TestAttrHelpersUseTheBoundedAttributeKeys(t *testing.T) {
	t.Parallel()

	if got := AttrApp("web"); got.Key != deploymentsv1.AttributeKey_ATTRIBUTE_KEY_APP || got.Value != "web" {
		t.Errorf("AttrApp() = %+v", got)
	}
	if got := AttrResourceCount(3); got.Key != deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT || got.Value != "3" {
		t.Errorf("AttrResourceCount() = %+v", got)
	}
	if got := AttrDurationMS(1500 * time.Millisecond); got.Key != deploymentsv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS || got.Value != "1500" {
		t.Errorf("AttrDurationMS() = %+v", got)
	}
	if got := AttrResourceType("aws:s3/bucket:Bucket"); got.Key != deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE || got.Value != "aws:s3/bucket:Bucket" {
		t.Errorf("AttrResourceType() = %+v", got)
	}
	if got := AttrResourceName("my-bucket"); got.Key != deploymentsv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME || got.Value != "my-bucket" {
		t.Errorf("AttrResourceName() = %+v", got)
	}
}
