package deploy

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/progressproto"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type phaseExtent struct {
	start time.Time
	end   time.Time
}

type deployMetrics struct {
	cfg    *project.Project
	target string
	dry    bool

	mu       sync.Mutex
	built    bool
	manifest *contractv1.Manifest
	deployed bool
	summary  *streamv1.RunSummary
	roots    map[string]bool
	phases   map[progressv1.Phase]phaseExtent
	actions  telemetry.PlanActions
}

func watchDeploy(bus *run.Bus, cfg *project.Project, target string, dry bool) *deployMetrics {
	metrics := &deployMetrics{cfg: cfg, target: target, dry: dry, roots: map[string]bool{}, phases: map[progressv1.Phase]phaseExtent{}}
	bus.Attach(metrics)
	return metrics
}

func (d *deployMetrics) noteManifest(manifest *contractv1.Manifest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.built = true
	d.manifest = manifest
}

func (d *deployMetrics) noteDeployed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deployed = true
}

func (d *deployMetrics) Receive(ev *streamv1.RunEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if summary := ev.GetSummary(); summary != nil {
		d.summary = summary
		return
	}
	op := ev.GetOperation()
	switch body := op.GetBody().(type) {
	case *progressv1.OperationEvent_Started:
		d.roots[string(op.GetSpanId())] = len(body.Started.GetParentSpanId()) == 0
	case *progressv1.OperationEvent_Ended:
		d.countAction(body.Ended)
		if d.roots[string(op.GetSpanId())] {
			d.extendPhase(op.GetPhase(), time.Unix(0, body.Ended.GetStartTimeUnixNano()), op.GetTime().AsTime())
		}
	}
}

func (d *deployMetrics) Close() error { return nil }

func (d *deployMetrics) countAction(ended *progressv1.Ended) {
	for _, attr := range ended.GetAttributes() {
		if key, known := progressproto.DecodeAttrKey(attr.GetKey()); !known || key != progress.AttrKeyResourceAction {
			continue
		}
		switch provider.ChangeAction(attr.GetValue()) {
		case provider.ActionCreate:
			d.actions.Create++
		case provider.ActionUpdate, provider.ActionReplace:
			d.actions.Update++
		case provider.ActionDelete, provider.ActionDisableThenDelete:
			d.actions.Delete++
		}
	}
}

func (d *deployMetrics) extendPhase(phase progressv1.Phase, start, end time.Time) {
	extent, seen := d.phases[phase]
	if !seen || start.Before(extent.start) {
		extent.start = start
	}
	if !seen || end.After(extent.end) {
		extent.end = end
	}
	d.phases[phase] = extent
}

func (d *deployMetrics) buildCompletion(err error) telemetry.DeployCompletion {
	d.mu.Lock()
	defer d.mu.Unlock()
	completion := telemetry.DeployCompletion{
		Success:        err == nil,
		Target:         d.target,
		AppCount:       len(d.cfg.Apps),
		ResourceCounts: countResourceKinds(d.manifest),
		PlanActions:    d.actions,
		PhaseDurations: map[string]time.Duration{},
		ErrorCode:      clierror.NewRunError(err).GetCode(),
	}
	if d.cfg.Provider != nil {
		completion.Provider = d.cfg.Provider.ID
	}
	for _, app := range d.cfg.Apps {
		if framework := app.Framework(); framework != "" {
			completion.Frameworks = append(completion.Frameworks, framework)
		}
		completion.Languages = append(completion.Languages, string(language.OfApp(app.Framework(), filepath.Join(d.cfg.Dir, app.Path))))
	}
	for phase, extent := range d.phases {
		if described, ok := run.DescribePhase(phase); ok {
			completion.PhaseDurations[described.Name] = extent.end.Sub(extent.start)
		}
	}
	for _, assumed := range d.summary.GetAssumed() {
		completion.Assumed = append(completion.Assumed, assumed.GetId())
	}
	return completion
}

func countResourceKinds(manifest *contractv1.Manifest) map[string]int {
	counts := map[string]int{}
	for _, resource := range manifest.GetResources() {
		counts[naming.ResourceTypeName(resource.GetResource().GetType())]++
	}
	return counts
}

func (d *deployMetrics) record(recordEvent func(telemetry.Payload) bool, err error) {
	d.mu.Lock()
	nothingToDeploy := d.built && d.manifest == nil
	ran := d.summary != nil && (err != nil || d.deployed || nothingToDeploy)
	deployed := d.deployed
	d.mu.Unlock()
	if d.dry || !ran {
		return
	}
	completion := d.buildCompletion(err)
	completion.FirstDeploy = !userconfig.HasDeployed()
	if recordEvent(completion) && err == nil && deployed && completion.FirstDeploy {
		_ = userconfig.MarkDeployed()
	}
}
