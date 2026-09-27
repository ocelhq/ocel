package runui_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerclient"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestMain(m *testing.M) {
	if os.Getenv(clitest.FakeProviderEnvVar) == "1" {
		os.Exit(clitest.RunFakeProvider())
	}
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

type fakeBody struct {
	ran bool
}

func (b *fakeBody) run(_ context.Context, _ *providerclient.Runner, ui *runui.Session) error {
	b.ran = true
	ui.Diagnostic("the body spoke")
	return nil
}

func specFor(t *testing.T, out *bytes.Buffer) runui.Spec {
	t.Helper()

	root, _ := clitest.SetUpDeployFixture(t)
	cfg, err := projectconfig.Resolve(context.Background(), root, "")
	if err != nil {
		t.Fatalf("projectconfig.Resolve() = %v", err)
	}
	return runui.Spec{
		Command: "ocel test",
		Config:  cfg,
		Present: runui.Resolve(runui.Origin{LogFormat: "human"}),
		Stdout:  out,
	}
}

func TestTheConvergentClassGatesNothingOfItsOwn(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Consent = consent.Convergent

	var body fakeBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.ran {
		t.Error("the body never ran, want a convergent command to reach it with no terminal and no --yes")
	}
	if !strings.Contains(out.String(), "the body spoke") {
		t.Errorf("stdout = %q, want the body's session writing to the command's stdout", out.String())
	}
}

func planFirstSpec(t *testing.T, out *bytes.Buffer) runui.Spec {
	t.Helper()

	spec := specFor(t, out)
	spec.Consent = consent.PlanFirst
	spec.Unattended = "pass --yes"
	return spec
}

func TestThePlanFirstClassRefusesOffATerminalAndSaysHowToProceed(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)

	var body fakeBody
	err := runui.Run(context.Background(), spec, body.run)
	if err == nil {
		t.Fatal("Run() = nil, want a refusal with no terminal to consent on")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("Run() = %q, want the refusal to name --yes", err)
	}
	if body.ran {
		t.Error("the body ran, want a destructive command stopped before it touches anything")
	}
}

func TestADryRunDrivesTheProviderThroughTheDriveThatWritesNothing(t *testing.T) {
	for _, tt := range []struct {
		name string
		dry  bool
		want string
	}{
		{"real", false, "Drive"},
		{"dry", true, "DriveDry"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			spec := specFor(t, &out)
			spec.Dry = tt.dry

			var drove string
			driving := func(name string) func(context.Context, *projectconfig.Config, io.Writer, io.Writer, providerclient.Trust, func(*providerclient.Runner) error) error {
				return func(_ context.Context, _ *projectconfig.Config, _, _ io.Writer, _ providerclient.Trust, fn func(*providerclient.Runner) error) error {
					drove = name
					return fn(nil)
				}
			}

			var body fakeBody
			if err := runui.RunDriving(context.Background(), spec, body.run, driving("Drive"), driving("DriveDry")); err != nil {
				t.Fatalf("Run() = %v", err)
			}
			if drove != tt.want {
				t.Errorf("Run() drove the provider through %q, want %q", drove, tt.want)
			}
		})
	}
}

func TestTheSessionTheBodyIsHandedHasTheResolvedPresentation(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Present = runui.Resolve(runui.Origin{LogFormat: "json"})

	var body fakeBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !strings.Contains(out.String(), `"message":"the body spoke"`) {
		t.Errorf("stdout = %q, want the body's session rendering as JSON because the command entry resolved it that way", out.String())
	}
}

func TestYesIsSilentWhereTheCommandRaisesNoGate(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Consent = consent.Convergent
	spec.Yes = true
	spec.Interactive = true
	spec.Stdin = strings.NewReader("")

	var body fakeBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.ran {
		t.Error("the body never ran, want --yes to change nothing where there is nothing to consent to")
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("stdout = %q, want --yes to raise no question of its own", out.String())
	}
}

func TestCtrlCFlushesTheBlockTheRunWasInsideOf(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	ctx, cancel := context.WithCancel(context.Background())

	err := runui.Run(ctx, spec, func(ctx context.Context, _ *providerclient.Runner, ui *runui.Session) error {
		ui.Building()
		if _, err := io.WriteString(ui.BuildWriter(), "Packages: +812\n▲ Next.js 15.4.2\n"); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})

	if code, ok := exitsig.ExitCode(err); !ok || code != exitsig.InterruptCode {
		t.Fatalf("Run() = %v (exit code %d), want the interrupt exit code %d", err, code, exitsig.InterruptCode)
	}
	if want := "⚠ Environment › Building interrupted\n"; !strings.Contains(out.String(), want) {
		t.Errorf("stdout = %q, want the in-flight block flushed under an interrupted marker:\n%s", out.String(), want)
	}
	if strings.Contains(out.String(), "Packages: +812") {
		t.Errorf("stdout = %q, want the builder's raw output left to the run log — a cancelled run is not a failed one", out.String())
	}
}

type planningBody struct {
	drawn     *planv1.ChangePlan
	consented *planv1.ChangePlan
	granted   bool
}

func (b *planningBody) run(ctx context.Context, _ *providerclient.Runner, ui *runui.Session) error {
	b.consented = ui.Plan("Proposed changes to the production bootstrap", b.drawn)
	granted, err := ui.Consent(ctx, "Apply these changes?")
	b.granted = granted
	return err
}

func mutatingPlan() *planv1.ChangePlan {
	return &planv1.ChangePlan{Groups: []*planv1.ChangeGroup{
		{Kind: "edge", Name: "cloudflare/edge", Action: planv1.Change_ACTION_CREATE},
		{Kind: "stack", Name: "aws/ocel-bootstrap", Action: planv1.Change_ACTION_KEEP, Reason: "already current"},
	}}
}

func TestTheApplyUsesThePlanTheRunShowed(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Present = runui.Resolve(runui.Origin{LogFormat: "json"})
	spec.Yes = true

	body := planningBody{drawn: mutatingPlan()}
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	shown := shownPlan(t, out.String())
	if !proto.Equal(shown, body.consented) {
		t.Errorf("the plan the body passes into the apply is\n%v\nand the plan the run showed is\n%v", body.consented, shown)
	}
	if body.consented.GetHeadline() != "Proposed changes to the production bootstrap" {
		t.Errorf("consented headline = %q, want the plan to keep the sentence it was shown under", body.consented.GetHeadline())
	}
	if first := body.consented.GetGroups()[0].GetKind(); first != "stack" {
		t.Errorf("the consented plan opens on a %q group, want the spine order the run showed, not the order the body drew", first)
	}
}

func shownPlan(t *testing.T, stream string) *planv1.ChangePlan {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(stream, "\n"), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		if plan := ev.GetPlan(); plan != nil {
			return plan
		}
	}
	t.Fatal("the run put no plan on the stream")
	return nil
}

func speakingProvider(stdout, stderr string, fails error) func(context.Context, *projectconfig.Config, io.Writer, io.Writer, providerclient.Trust, func(*providerclient.Runner) error) error {
	return func(_ context.Context, _ *projectconfig.Config, out, errOut io.Writer, _ providerclient.Trust, fn func(*providerclient.Runner) error) error {
		_, _ = io.WriteString(out, stdout+"\n")
		_, _ = io.WriteString(errOut, stderr+"\n")
		if fails != nil {
			return fails
		}
		return fn(nil)
	}
}

func TestALineTheProviderProcessWritesIsADebugOutputLineNamingTheProviderAndItsStream(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Present = runui.Resolve(runui.Origin{LogFormat: "json"})

	drive := speakingProvider("a line on stdout", "a line on stderr", nil)
	if err := runui.RunDriving(context.Background(), spec, (&fakeBody{}).run, drive, drive); err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for want, stream := range map[string]progressv1.Stream{
		"a line on stdout": progressv1.Stream_STREAM_STDOUT,
		"a line on stderr": progressv1.Stream_STREAM_STDERR,
	} {
		ev := eventCarrying(t, out.String(), want)
		if ev.GetLevel() != progressv1.Level_LEVEL_DEBUG || ev.GetSubject() != "aws" {
			t.Errorf("the provider's %q landed as level %s subject %q, want a DEBUG event whose subject is the provider %q", want, ev.GetLevel(), ev.GetSubject(), "aws")
		}
		if got := ev.GetOutput(); got == nil || got.GetStream() != stream {
			t.Errorf("the provider's %q landed as %T on %v, want an output line on %v", want, ev.GetBody(), got.GetStream(), stream)
		}
	}
}

func eventCarrying(t *testing.T, stream, message string) *streamv1.RunEvent {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stream), "\n") {
		var ev streamv1.RunEvent
		if err := protojson.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("protojson.Unmarshal(%q) = %v", line, err)
		}
		if ev.GetMessage() == message {
			return &ev
		}
	}
	t.Fatalf("stream = %q, want an event whose message is %q", stream, message)
	return nil
}

func TestAProviderThatDiesBeforeReadyStillShowsItsStderrInTheFailure(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)

	crash := "panic: the provider could not read its credentials"
	drive := speakingProvider("", crash, &providerclient.EarlyExitError{Stderr: crash})
	err := runui.RunDriving(context.Background(), spec, (&fakeBody{}).run, drive, drive)

	if code, ok := exitsig.ExitCode(err); !ok || code != 1 {
		t.Fatalf("Run() = %v (exit code %d), want the failure exit code 1", err, code)
	}
	if got := strings.Count(out.String(), crash); got != 1 {
		t.Errorf("stdout = %q shows the provider's stderr %d times, want it once, in the failure, though its raw output is debug", out.String(), got)
	}
}
