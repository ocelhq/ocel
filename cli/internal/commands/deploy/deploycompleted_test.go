package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	namedApp      = "zebra-storefront"
	namedResource = "quartz-uploads"
)

func enableTelemetry(t *testing.T) {
	t.Helper()
	confighome.Isolate(t)
	t.Setenv(telemetry.EnvVar, "debug")
}

type recordedEvents struct {
	payloads []telemetry.Payload
}

func recordEvents(dependencies *Dependencies) *recordedEvents {
	recorded := &recordedEvents{}
	dependencies.RecordEvent = func(payload telemetry.Payload) bool {
		recorded.payloads = append(recorded.payloads, payload)
		return true
	}
	return recorded
}

func (r *recordedEvents) onlyDeployCompleted(t *testing.T) telemetry.Event {
	t.Helper()
	if len(r.payloads) != 1 {
		t.Fatalf("recorded %d events, want exactly 1: %v", len(r.payloads), r.payloads)
	}
	event, err := telemetry.NewEvent(telemetry.NewIdentity("an-install-id"), time.Now(), r.payloads[0])
	if err != nil {
		t.Fatal(err)
	}
	if event.Name != "deploy_completed" {
		t.Fatalf("recorded a %s event, want deploy_completed", event.Name)
	}
	var sent telemetry.Event
	if err := json.Unmarshal([]byte(marshalJSON(t, event)), &sent); err != nil {
		t.Fatal(err)
	}
	return sent
}

func setUpNamedAppProject(t *testing.T) (clitest.FakeProject, Dependencies) {
	t.Helper()
	fixture := setUpPreviewProject(t)
	writeAppsConfig(t, fixture.Root, `{ name: "`+namedApp+`", path: "apps/`+namedApp+`", framework: "node" }`)
	writeAppSource(t, fixture.Root, namedApp)
	dependencies := previewDependencies("feature/login", "")
	stubBuild(&dependencies, []build.Function{
		{Route: namedApp, Framework: apiFunction()[0].Framework, EntryFile: "src/server.js", ArtifactPath: "output/" + namedApp, App: namedApp},
	})
	return fixture, dependencies
}

func runDeployFor(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts deployOptions) error {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	return runDeploy(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader(""))
}

func TestADeployRecordsADeployCompletedEventWithNoNames(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	source := filepath.Join(clitest.DiscoveryDir(fixture.Root), "storage.ts") + ":4"
	dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
		return []declaration.Resource{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET, Name: namedResource, Bucket: &resourcesv1.BucketConfig{}, Source: source}}, nil
	}

	recorded := recordEvents(&dependencies)
	err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v", err)
	}
	event := recorded.onlyDeployCompleted(t)

	wantProperties := []string{
		"app_count", "arch", "agent", "assumed", "ci", "cli_version", "error_code", "first_deploy", "frameworks", "languages",
		"os", "phase_ms", "plan_actions", "$process_person_profile", "provider", "resource_counts", "success", "target",
	}
	if got := slices.Sorted(maps.Keys(event.Properties)); !slices.Equal(got, slices.Sorted(slices.Values(wantProperties))) {
		t.Errorf("properties = %v, want %v", got, slices.Sorted(slices.Values(wantProperties)))
	}
	for property, want := range map[string]any{
		"success":         true,
		"target":          "production",
		"provider":        "fake",
		"frameworks":      []any{"node"},
		"languages":       []any{"js"},
		"app_count":       float64(1),
		"resource_counts": map[string]any{"bucket": float64(1)},
		"error_code":      "",
		"assumed":         []any{},
	} {
		if got := marshalJSON(t, event.Properties[property]); got != marshalJSON(t, want) {
			t.Errorf("%s = %s, want %s", property, got, marshalJSON(t, want))
		}
	}
	for _, name := range []string{namedApp, namedResource, clitest.FixtureSlug, fixture.Root} {
		if line := marshalJSON(t, event); strings.Contains(line, name) {
			t.Errorf("event %s names %q", line, name)
		}
	}
}

func TestADeployCompletedEventTimesTheDeploysPhasesByTheirNames(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)

	recorded := recordEvents(&dependencies)
	err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v", err)
	}

	phases, _ := recorded.onlyDeployCompleted(t).Properties["phase_ms"].(map[string]any)
	for _, phase := range []string{"check", "build"} {
		if _, timed := phases[phase]; !timed {
			t.Errorf("phase_ms = %v, want %q timed", phases, phase)
		}
	}
	for phase, ms := range phases {
		if _, known := phaseNames()[phase]; !known {
			t.Errorf("phase_ms key %q is no phase", phase)
		}
		if ms.(float64) < 0 {
			t.Errorf("phase_ms[%s] = %v, want a duration", phase, ms)
		}
	}
}

func phaseNames() map[string]bool {
	names := map[string]bool{}
	for phase := range progressv1.Phase_name {
		if described, ok := run.DescribePhase(progressv1.Phase(phase)); ok {
			names[described.Name] = true
		}
	}
	return names
}

func TestAFailedDeployCarriesItsErrorCodeAndNoErrorText(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, &clierror.Error{Code: clierror.CodeProviderUnavailable, Cause: errors.New("cannot compile /home/someone/secret-path/app.ts")}
	}

	recorded := recordEvents(&dependencies)
	err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy succeeded through a failed build")
	}
	event := recorded.onlyDeployCompleted(t)

	if event.Properties["success"] != false || event.Properties["error_code"] != clierror.CodeProviderUnavailable {
		t.Errorf("success, error_code = %v, %v, want false and the build's code", event.Properties["success"], event.Properties["error_code"])
	}
	if line := marshalJSON(t, event); strings.Contains(line, "secret-path") || strings.Contains(line, "cannot compile") {
		t.Errorf("event %s carries the error text", line)
	}
}

func TestAFailedDeployWithNoCodeReportsInternal(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, errors.New("simulated build failure")
	}

	recorded := recordEvents(&dependencies)
	_ = runDeployFor(t, fixture, dependencies, deployOptions{yes: true})

	if got := recorded.onlyDeployCompleted(t).Properties["error_code"]; got != "internal" {
		t.Errorf("error_code = %v, want internal", got)
	}
}

func TestADeployCancelledMidRunReportsItAsInterrupted(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		cancel()
		return build.Output{}, ctx.Err()
	}

	recorded := recordEvents(&dependencies)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(ctx, dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded through a cancelled build")
	}

	event := recorded.onlyDeployCompleted(t)
	if event.Properties["success"] != false || event.Properties["error_code"] != clierror.CodeInterrupted {
		t.Errorf("success, error_code = %v, %v, want false and %q", event.Properties["success"], event.Properties["error_code"], clierror.CodeInterrupted)
	}
}

func TestAPreviewDeployRecordsAPreviewTarget(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	opts := previewUpOptions{name: "staging", persistent: true}
	coverEphemeralPreview(t, fixture, dependencies, opts)

	recorded := recordEvents(&dependencies)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	event := recorded.onlyDeployCompleted(t)

	if event.Properties["target"] != "preview" || event.Properties["success"] != true || event.Properties["provider"] != "fake" {
		t.Errorf("target, success, provider = %v, %v, %v, want preview, true and fake", event.Properties["target"], event.Properties["success"], event.Properties["provider"])
	}
	for _, name := range []string{namedApp, "staging", "feature/login", clitest.FixtureSlug} {
		if line := marshalJSON(t, event); strings.Contains(line, name) {
			t.Errorf("event %s names %q", line, name)
		}
	}
}

func TestAFailedPreviewDeployCarriesItsErrorCode(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, &clierror.Error{Code: clierror.CodeProviderUnavailable}
	}
	opts := previewUpOptions{name: "staging", persistent: true}

	recorded := recordEvents(&dependencies)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runPreviewUp succeeded through a failed build")
	}

	event := recorded.onlyDeployCompleted(t)
	if event.Properties["target"] != "preview" || event.Properties["error_code"] != clierror.CodeProviderUnavailable {
		t.Errorf("target, error_code = %v, %v, want preview and the build's code", event.Properties["target"], event.Properties["error_code"])
	}
}

func TestOnlyTheFirstSuccessfulDeployOfAnInstallIsAFirstDeploy(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)

	first := recordEvents(&dependencies)
	if err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true}); err != nil {
		t.Fatal(err)
	}
	second := recordEvents(&dependencies)
	if err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true}); err != nil {
		t.Fatal(err)
	}

	if got := first.onlyDeployCompleted(t).Properties["first_deploy"]; got != true {
		t.Errorf("first deploy's first_deploy = %v, want true", got)
	}
	if got := second.onlyDeployCompleted(t).Properties["first_deploy"]; got != false {
		t.Errorf("second deploy's first_deploy = %v, want false", got)
	}
}

func TestADeployThatFailedIsStillTheInstallsFirstDeployUntilOneSucceeds(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{}, errors.New("simulated build failure")
	}

	failed := recordEvents(&dependencies)
	_ = runDeployFor(t, fixture, dependencies, deployOptions{yes: true})
	if userconfig.HasDeployed() {
		t.Fatal("a failed deploy marked the install as having deployed")
	}
	dependencies.BuildApps = buildApps
	retried := recordEvents(&dependencies)
	if err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true}); err != nil {
		t.Fatal(err)
	}

	if got := failed.onlyDeployCompleted(t).Properties["first_deploy"]; got != true {
		t.Errorf("failed deploy's first_deploy = %v, want true", got)
	}
	if got := retried.onlyDeployCompleted(t).Properties["first_deploy"]; got != true {
		t.Errorf("retried deploy's first_deploy = %v, want true", got)
	}
	if !userconfig.HasDeployed() {
		t.Error("a successful deploy did not mark the install as having deployed")
	}
}

func TestADeployThatAssumedAGuardListsItsID(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	recorded := recordEvents(&dependencies)
	err := runDeployFor(t, fixture, dependencies, deployOptions{})
	if err != nil {
		t.Fatal(err)
	}

	event := recorded.onlyDeployCompleted(t)
	if got := marshalJSON(t, event.Properties["assumed"]); got != `["new_project"]` {
		t.Errorf("assumed = %s, want [\"new_project\"]", got)
	}
	if line := marshalJSON(t, event); strings.Contains(line, "my-application") || strings.Contains(line, "billing") {
		t.Errorf("event %s names the projects the backend holds", line)
	}
}

func TestADryDeployRecordsNoDeployCompletedEvent(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)

	recorded := recordEvents(&dependencies)
	err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true, dry: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(recorded.payloads) != 0 {
		t.Errorf("a dry deploy recorded %d events, want none: it deployed nothing", len(recorded.payloads))
	}
	if userconfig.HasDeployed() {
		t.Error("a dry deploy marked the install as having deployed")
	}
}

func TestADeployWithNothingToDeployRecordsASuccessThatLeavesTheInstallsFirstDeployAhead(t *testing.T) {
	enableTelemetry(t)
	fixture := setUpDeployProject(t)
	writeConfig(t, fixture.Root, "")
	clitest.WriteFile(t, clitest.DiscoveryDir(fixture.Root)+"/main.ts", "export {};\n")
	dependencies := newTestDependencies()

	recorded := recordEvents(&dependencies)
	err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true})
	if err != nil {
		t.Fatal(err)
	}

	event := recorded.onlyDeployCompleted(t)
	if event.Properties["success"] != true || event.Properties["target"] != "production" || event.Properties["first_deploy"] != true {
		t.Errorf("success, target, first_deploy = %v, %v, %v, want true, production and true", event.Properties["success"], event.Properties["target"], event.Properties["first_deploy"])
	}
	if userconfig.HasDeployed() {
		t.Error("a deploy with nothing to deploy marked the install as having deployed: the next real deploy would not be its first")
	}
}

func TestAPreviewDeployWithNothingToDeployRecordsASuccessThatLeavesTheInstallsFirstDeployAhead(t *testing.T) {
	enableTelemetry(t)
	fixture := setUpPreviewProject(t)
	writeConfig(t, fixture.Root, "")
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(fixture.Root), "main.ts"), "export {};\n")
	dependencies := previewDependencies("feature/login", "")

	recorded := recordEvents(&dependencies)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{name: "staging", persistent: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewUp err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	event := recorded.onlyDeployCompleted(t)
	if event.Properties["success"] != true || event.Properties["target"] != "preview" || event.Properties["first_deploy"] != true {
		t.Errorf("success, target, first_deploy = %v, %v, %v, want true, preview and true", event.Properties["success"], event.Properties["target"], event.Properties["first_deploy"])
	}
	if userconfig.HasDeployed() {
		t.Error("a preview deploy with nothing to deploy marked the install as having deployed: the next real deploy would not be its first")
	}
}

func TestAFirstDeployWhoseEventWasNotRecordedStoresNothingInTheUserConfig(t *testing.T) {
	enableTelemetry(t)
	fixture, dependencies := setUpNamedAppProject(t)
	dependencies.RecordEvent = func(telemetry.Payload) bool { return false }

	if err := runDeployFor(t, fixture, dependencies, deployOptions{yes: true}); err != nil {
		t.Fatal(err)
	}

	if settings := userconfig.Read(); len(settings) != 0 {
		t.Errorf("settings = %v, want nothing stored: the next deploy is still the first one recorded", settings)
	}
}

func TestADeployCompletionCountsTheResourceActionsTheRunApplied(t *testing.T) {
	bus := run.NewBus(newSteppingClock())
	metrics := watchDeploy(bus, &project.Project{}, telemetry.DeployTargetProduction, false)
	_, running, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	provision := running.Phase(progressv1.Phase_PHASE_PROVISION)
	for _, action := range []string{"create", "create", "update", "replace", "delete", "disable-then-delete", "keep", "adopt", ""} {
		attrs := []progress.Attr{}
		if action != "" {
			attrs = append(attrs, progress.Attr{Key: progress.AttrKeyResourceAction, Value: action})
		}
		provision.Trace("subject", "resource operation", attrs...).End(nil)
	}
	provision.End(nil)
	var runErr error
	running.End(&runErr)

	got := metrics.buildCompletion(nil).PlanActions

	if want := (telemetry.PlanActions{Create: 2, Update: 2, Delete: 2}); got != want {
		t.Errorf("plan actions = %+v, want %+v: a replace changes a resource and a disable-then-delete removes one", got, want)
	}
}

func TestADeployCompletionTimesEachPhaseFromItsFirstStartToItsLastEnd(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	bus := run.NewBus(func() time.Time { return now })
	metrics := watchDeploy(bus, &project.Project{}, telemetry.DeployTargetProduction, false)
	_, running, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	build := running.Phase(progressv1.Phase_PHASE_BUILD)
	now = now.Add(2 * time.Second)
	child := build.Child("api", progress.Title{})
	now = now.Add(5 * time.Second)
	child.End(nil)
	now = now.Add(time.Second)
	build.End(nil)
	now = now.Add(time.Second)
	provision := running.Phase(progressv1.Phase_PHASE_PROVISION)
	now = now.Add(3 * time.Second)
	provision.End(nil)
	var runErr error
	running.End(&runErr)

	got := metrics.buildCompletion(nil).PhaseDurations

	want := map[string]time.Duration{"build": 8 * time.Second, "provision": 3 * time.Second}
	if !maps.Equal(got, want) {
		t.Errorf("phase durations = %v, want %v: a child span lies inside its phase and the gap between phases is no phase", got, want)
	}
}

func TestADeployCompletionListsTheFrameworksAndLanguagesOfItsApps(t *testing.T) {
	cfg := &project.Project{
		Dir: t.TempDir(),
		Apps: []project.App{
			{Name: "a", Path: "a", Serverless: &project.Serverless{Framework: "next"}},
			{Name: "b", Path: "b", Serverless: &project.Serverless{Framework: "node"}},
			{Name: "c", Path: "c", Serverless: &project.Serverless{Framework: "python"}},
			{Name: "d", Path: "d", Serverless: &project.Serverless{Framework: "node"}},
		},
	}
	metrics := watchDeploy(run.NewBus(time.Now), cfg, telemetry.DeployTargetPreview, false)

	event, err := telemetry.NewEvent(telemetry.Identity{}, time.Now(), metrics.buildCompletion(nil))
	if err != nil {
		t.Fatal(err)
	}

	if got := marshalJSON(t, event.Properties["frameworks"]); got != `["next","node","python"]` {
		t.Errorf("frameworks = %s, want next, node and python once each", got)
	}
	if got := marshalJSON(t, event.Properties["languages"]); got != `["js","python"]` {
		t.Errorf("languages = %s, want js and python once each", got)
	}
	if event.Properties["app_count"] != 4 || event.Properties["target"] != telemetry.DeployTargetPreview {
		t.Errorf("app count, target = %v, %v, want 4 and preview", event.Properties["app_count"], event.Properties["target"])
	}
}

func newSteppingClock() func() time.Time {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		now = now.Add(time.Millisecond)
		return now
	}
}

func marshalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
