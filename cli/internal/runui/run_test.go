package runui_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
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
