package prerequisite_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type missingProject struct{}

func (missingProject) Error() string {
	return "no ocel.json found here.\nRun `ocel init` and try again"
}
func (missingProject) Missing() prerequisite.Kind { return prerequisite.Project }
func (missingProject) Finding() string            { return "No ocel.json here." }
func (missingProject) Remedy() string             { return "`ocel init`" }

type missingBootstrap struct{}

func (missingBootstrap) Error() string {
	return "no infrastructure is set up yet.\nRun `ocel bootstrap production` and try again"
}
func (missingBootstrap) Missing() prerequisite.Kind { return prerequisite.Bootstrap }
func (missingBootstrap) Finding() string            { return "The account has no Ocel infrastructure yet." }
func (missingBootstrap) Remedy() string             { return "`ocel bootstrap production`" }

func checkSpan(t *testing.T) *run.Span {
	t.Helper()
	_, running, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return running.Phase(progressv1.Phase_PHASE_CHECK)
}

type recordedSetup struct {
	runs int
}

func (r *recordedSetup) setup(changesAccount bool, fixes func()) prerequisite.Setup {
	return prerequisite.Setup{
		Prompt:         func(missing prerequisite.MissingError) string { return "Run " + missing.Remedy() + " now?" },
		ChangesAccount: changesAccount,
		Run: func(context.Context, consent.Policy, *run.Span, prerequisite.MissingError) error {
			r.runs++
			fixes()
			return nil
		},
	}
}

func terminalPolicy(answers string, out *bytes.Buffer) consent.Policy {
	return consent.Policy{Command: "ocel deploy", Interactive: true, In: strings.NewReader(answers), Out: out}
}

func TestAMissingProjectIsSetUpAndCheckedAgain(t *testing.T) {
	present := false
	checks := 0
	check := func(context.Context) error {
		checks++
		if present {
			return nil
		}
		return missingProject{}
	}
	recorded := &recordedSetup{}
	setups := prerequisite.Setups{prerequisite.Project: recorded.setup(false, func() { present = true })}

	var out bytes.Buffer
	if err := setups.Ensure(context.Background(), terminalPolicy("y\n", &out), checkSpan(t), check); err != nil {
		t.Fatalf("Ensure err = %v, want the project set up and the check passed", err)
	}
	if recorded.runs != 1 {
		t.Errorf("the setup ran %d times, want once", recorded.runs)
	}
	if checks != 2 {
		t.Errorf("the check ran %d times, want twice: once to find the project missing, once after its setup", checks)
	}
	if !strings.Contains(out.String(), "Run `ocel init` now?") {
		t.Errorf("asked %q, want the setup's prompt put to the terminal", out.String())
	}
}

func TestADeclinedSetupChangesNothingAndNamesTheCommand(t *testing.T) {
	recorded := &recordedSetup{}
	setups := prerequisite.Setups{prerequisite.Bootstrap: recorded.setup(true, func() {})}

	var out bytes.Buffer
	err := setups.Ensure(context.Background(), terminalPolicy("n\n", &out), checkSpan(t), func(context.Context) error {
		return missingBootstrap{}
	})
	if !prerequisite.IsDeclined(err) {
		t.Fatalf("Ensure err = %v, want the decline reported as one", err)
	}
	if recorded.runs != 0 {
		t.Errorf("the setup ran %d times after it was declined, want never", recorded.runs)
	}
	want := "Not set up, so this run changes nothing. When you're ready: `ocel bootstrap production`"
	if err.Error() != want {
		t.Errorf("decline = %q, want %q", err, want)
	}
}

func TestAPrerequisiteStillMissingAfterItsSetupIsAnError(t *testing.T) {
	recorded := &recordedSetup{}
	setups := prerequisite.Setups{prerequisite.Bootstrap: recorded.setup(true, func() {})}

	var out bytes.Buffer
	err := setups.Ensure(context.Background(), terminalPolicy("y\n", &out), checkSpan(t), func(context.Context) error {
		return missingBootstrap{}
	})
	var missing missingBootstrap
	if !errors.As(err, &missing) {
		t.Fatalf("Ensure err = %v, want the prerequisite its setup left missing returned", err)
	}
	if recorded.runs != 1 {
		t.Errorf("the setup ran %d times, want once: a kind is set up at most once per run", recorded.runs)
	}
}

func TestADryRunNeverRunsASetupThatChangesTheAccount(t *testing.T) {
	recorded := &recordedSetup{}
	setups := prerequisite.Setups{prerequisite.Bootstrap: recorded.setup(true, func() {})}

	var out bytes.Buffer
	policy := terminalPolicy("y\n", &out)
	policy.DryRun = true
	err := setups.Ensure(context.Background(), policy, checkSpan(t), func(context.Context) error {
		return missingBootstrap{}
	})
	if !strings.Contains(err.Error(), "Run `ocel bootstrap production` and try again") {
		t.Errorf("Ensure err = %v, want the command named", err)
	}
	if recorded.runs != 0 || out.Len() != 0 {
		t.Errorf("a dry run ran the setup %d times and asked %q, want neither", recorded.runs, out.String())
	}
}

func TestWithoutATerminalNothingIsAskedAndTheErrorNamesTheCommand(t *testing.T) {
	recorded := &recordedSetup{}
	setups := prerequisite.Setups{prerequisite.Project: recorded.setup(false, func() {})}

	var out bytes.Buffer
	policy := terminalPolicy("y\n", &out)
	policy.Interactive = false
	err := setups.Ensure(context.Background(), policy, checkSpan(t), func(context.Context) error {
		return missingProject{}
	})
	if err == nil || !strings.HasSuffix(err.Error(), "Run `ocel init` and try again") {
		t.Errorf("Ensure err = %v, want it to end naming the command", err)
	}
	if recorded.runs != 0 || out.Len() != 0 {
		t.Errorf("without a terminal the setup ran %d times and asked %q, want neither", recorded.runs, out.String())
	}
}
