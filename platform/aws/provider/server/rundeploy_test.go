package server

import (
	"context"
	"sync"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recordedDeclare struct {
	final  bool
	stages []deploy.Stage
}

type recordedSpan struct {
	id, parentID deploy.StageID
	name         string
	failed       bool
}

type fakeTracer struct {
	mu       sync.Mutex
	declared []recordedDeclare
	spans    []recordedSpan
}

func (f *fakeTracer) DeclareStages(final bool, stages ...deploy.Stage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.declared = append(f.declared, recordedDeclare{final: final, stages: stages})
}

func (f *fakeTracer) Span(id, parentID deploy.StageID, name string, start, end time.Time, err error, attrs ...deploy.Attr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, recordedSpan{id: id, parentID: parentID, name: name, failed: err != nil})
}

func TestRunDeployDeclaresTheStagePlanBeforeAnyWork(t *testing.T) {
	t.Parallel()

	s := &Server{}
	tracer := &fakeTracer{}
	req := &contractv1.DeployRequest{Manifest: wellFormedManifest()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stages := newDeployStages()
	appStages, appDeclared := deploy.AppStages(stages.provisioning, req.GetManifest())
	_, err := s.runDeploy(ctx, req, req.GetManifest(), nil, stages, appStages, appDeclared, noopProgress, noopStageReport, noopLog, noopDegraded, tracer)
	if err == nil {
		t.Fatal("runDeploy() error = nil, want the first reach for AWS to fail")
	}

	if len(tracer.declared) == 0 {
		t.Fatal("no stage plan was declared before runDeploy failed")
	}
	first := tracer.declared[0]
	if !first.final {
		t.Error("first declared batch has Final = false, want true: the app list is already known from the manifest")
	}
	wantTitles := []string{"Preparing", "Uploading", "Provisioning", "Finalizing"}
	if len(first.stages) != len(wantTitles) {
		t.Fatalf("got %d top-level stages, want %d", len(first.stages), len(wantTitles))
	}
	for i, want := range wantTitles {
		if got := first.stages[i].Title; got != want {
			t.Errorf("stage[%d].Title = %q, want %q", i, got, want)
		}
	}

	if len(tracer.spans) != 1 {
		t.Fatalf("got %d spans, want 1 (Preparing, failed)", len(tracer.spans))
	}
	preparing := first.stages[0]
	if tracer.spans[0].id != preparing.ID {
		t.Error("the recorded span's id does not match the declared Preparing stage's id")
	}
	if !tracer.spans[0].failed {
		t.Error("the Preparing span was not recorded as failed")
	}
}

func noopProgress(progressv1.Phase, string, uint32, uint32) {}
func noopLog(string)                                        {}
func noopStageReport(deploy.StageID) func(string)           { return func(string) {} }
func noopDegraded(edge.Need, string)                        {}
