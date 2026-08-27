package runui_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provider"
	"github.com/ocelhq/ocel/cli/internal/runui"
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

func (b *fakeBody) run(_ context.Context, _ *provider.Runner, ui *runui.Session) error {
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
	spec.Consent = runui.Convergent

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
	spec.Consent = runui.PlanFirst
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

func TestThePlanFirstClassTakesYesInPlaceOfATerminal(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Yes = true

	var body fakeBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.ran {
		t.Error("the body never ran, want --yes to stand in for the terminal")
	}
}

func TestADryRunNeedsNoConsentAtAll(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Dry = true

	var body fakeBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.ran {
		t.Error("the body never ran, want --dry to reach the plan with nothing to consent to")
	}
}

func TestTheSessionTheBodyIsHandedCarriesTheResolvedPresentation(t *testing.T) {
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

func TestThePlanFirstClassTakesATerminalInPlaceOfYes(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Interactive = true

	var body fakeBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.ran {
		t.Error("the body never ran, want a terminal to be consent enough to reach the plan")
	}
}

type guardedBody struct {
	granted bool
}

func (b *guardedBody) run(ctx context.Context, _ *provider.Runner, ui *runui.Session) error {
	granted, err := ui.Guard(ctx, `Tear down the named preview "staging"?`)
	b.granted = granted
	return err
}

func TestAConvergentGuardIsGrantedInAdvanceByYes(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Consent = runui.Convergent
	spec.Yes = true
	spec.Interactive = true
	spec.Stdin = strings.NewReader("n\n")

	var body guardedBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.granted {
		t.Error("the guard was not granted, want --yes to answer it in advance")
	}
	if strings.Contains(out.String(), "Tear down") {
		t.Errorf("stdout = %q, want --yes to leave the guard unasked", out.String())
	}
}

func TestYesIsSilentWhereTheCommandRaisesNoGate(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Consent = runui.Convergent
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

func TestAConvergentGuardSkipsWhenThereIsNoTerminalToAskOn(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Consent = runui.Convergent
	spec.Stdin = strings.NewReader("")

	var body guardedBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.granted {
		t.Error("the guard stopped the body, want a guard to skip and proceed off a terminal")
	}
	if strings.Contains(out.String(), "Tear down") {
		t.Errorf("stdout = %q, want no question asked where nothing can answer it", out.String())
	}
}

func TestAConvergentGuardIsAskedOnATerminalAndANoStopsTheCommand(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	spec.Consent = runui.Convergent
	spec.Interactive = true
	spec.Stdin = strings.NewReader("n\n")

	var body guardedBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if body.granted {
		t.Error("the guard was granted, want the answered no to withhold it")
	}
	if !strings.Contains(out.String(), "Tear down the named preview") {
		t.Errorf("stdout = %q, want the guard's question put to the terminal", out.String())
	}
	if !strings.Contains(out.String(), "Aborted.") {
		t.Errorf("stdout = %q, want a declined guard to say the command stopped", out.String())
	}
}

type consentingBody struct {
	granted bool
}

func (b *consentingBody) run(ctx context.Context, _ *provider.Runner, ui *runui.Session) error {
	granted, err := ui.ConsentByName(ctx, "project name", "acme")
	b.granted = granted
	return err
}

func TestPlanConsentIsGrantedInAdvanceByYesWithoutAskingAgain(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Yes = true
	spec.Stdin = strings.NewReader("")

	var body consentingBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.granted {
		t.Error("consent was withheld, want --yes to grant the gate this class raises")
	}
	if strings.Contains(out.String(), "project name") {
		t.Errorf("stdout = %q, want --yes to leave the ceremony unasked", out.String())
	}
}

func TestPlanConsentOnATerminalIsTheTypedNameCeremony(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Interactive = true
	spec.Stdin = strings.NewReader("acme\n")

	var body consentingBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if !body.granted {
		t.Error("consent was withheld, want the typed name to grant it")
	}
	if !strings.Contains(out.String(), "project name") {
		t.Errorf("stdout = %q, want the ceremony to name what has to be typed", out.String())
	}
}

func TestPlanConsentIsWithheldWhenTheNameIsNotTypedBack(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Interactive = true
	spec.Stdin = strings.NewReader("something else\n")

	var body consentingBody
	if err := runui.Run(context.Background(), spec, body.run); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if body.granted {
		t.Error("consent was granted, want a mistyped name to withhold it")
	}
	if !strings.Contains(out.String(), "Aborted.") {
		t.Errorf("stdout = %q, want a withheld consent to say the command stopped", out.String())
	}
}

func TestThePlanFirstRefusalReadsTrueForACommandThatCreates(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Command = "ocel bootstrap production"

	err := runui.Run(context.Background(), spec, (&fakeBody{}).run)
	if err == nil {
		t.Fatal("Run() = nil, want a refusal with no terminal to consent on")
	}
	if strings.Contains(err.Error(), "remove") {
		t.Errorf("Run() = %q, want a refusal that describes the plan, not one that assumes it destroys", err)
	}
}

type askingBody struct {
	asking bool
}

func (b *askingBody) run(_ context.Context, _ *provider.Runner, ui *runui.Session) error {
	b.asking = ui.Asking()
	return nil
}

func TestYesTakesTheCommandOutOfTheAskingBusinessAltogether(t *testing.T) {
	for _, tc := range []struct {
		name        string
		yes         bool
		interactive bool
		want        bool
	}{
		{"a terminal and no --yes", false, true, true},
		{"a terminal with --yes", true, true, false},
		{"no terminal", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			spec := specFor(t, &out)
			spec.Yes, spec.Interactive = tc.yes, tc.interactive

			var body askingBody
			if err := runui.Run(context.Background(), spec, body.run); err != nil {
				t.Fatalf("Run() = %v", err)
			}
			if body.asking != tc.want {
				t.Errorf("Asking() = %v, want %v", body.asking, tc.want)
			}
		})
	}
}

func TestCtrlCFlushesTheBlockTheRunWasInsideOf(t *testing.T) {
	var out bytes.Buffer
	spec := specFor(t, &out)
	ctx, cancel := context.WithCancel(context.Background())

	err := runui.Run(ctx, spec, func(ctx context.Context, _ *provider.Runner, ui *runui.Session) error {
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
	want := "  Packages: +812\n  ▲ Next.js 15.4.2\n⚠ Environment › Building interrupted\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("stdout = %q, want the in-flight block flushed whole under an interrupted marker:\n%s", out.String(), want)
	}
}

func TestTheRefusalNamesTheCommandAndTheRemedyThatCommandOffers(t *testing.T) {
	var out bytes.Buffer
	spec := planFirstSpec(t, &out)
	spec.Command = "ocel destroy production"
	spec.Unattended = "set OCEL_DESTROY_BYPASS_CONFIRMATION to the project name"

	err := runui.Run(context.Background(), spec, (&fakeBody{}).run)
	if err == nil {
		t.Fatal("Run() = nil, want a refusal with no terminal to consent on")
	}
	if !strings.Contains(err.Error(), "ocel destroy production") {
		t.Errorf("Run() = %q, want the refusal to name the command that was refused", err)
	}
	if !strings.Contains(err.Error(), "OCEL_DESTROY_BYPASS_CONFIRMATION") {
		t.Errorf("Run() = %q, want the refusal to offer the remedy this command actually has, not --yes", err)
	}
}
