package consent_test

import (
	"fmt"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/consent"
)

func TestABypassNamingAnotherSubjectOnATerminalFallsBackToConfirmingAndSaysWhy(t *testing.T) {
	t.Setenv(consent.BypassEnv, "shop")

	granted, notice, err := consent.Bypass{Noun: "project", Subject: "acme", Action: "destroying production", Verb: "destroyed", Interactive: true}.Granted()

	if err != nil || granted {
		t.Fatalf("Granted() = %v, %v, want no grant and no error", granted, err)
	}
	if want := consent.BypassEnv + ` is set to "shop", not this project (acme); confirming interactively instead`; notice != want {
		t.Errorf("notice = %q, want %q", notice, want)
	}
}

func TestABypassWithNothingSetGrantsNothingAndSaysNothing(t *testing.T) {
	t.Setenv(consent.BypassEnv, "")

	granted, notice, err := consent.Bypass{Noun: "project", Subject: "acme", Action: "destroying production", Verb: "destroyed"}.Granted()

	if err != nil || granted || notice != "" {
		t.Errorf("Granted() = %v, %q, %v, want no grant, no notice and no error", granted, notice, err)
	}
}

func TestABypassNamingAnotherSubjectWithoutATerminalFailsWithConfirmationBypassMismatch(t *testing.T) {
	t.Setenv(consent.BypassEnv, "shop")

	_, _, err := consent.Bypass{Noun: "project", Subject: "acme", Action: "destroying production", Verb: "destroyed"}.Granted()

	got := clierror.NewRunError(fmt.Errorf("destroy: %w", err))
	if got.GetCode() != clierror.CodeConfirmationBypassMismatch {
		t.Fatalf("code = %q, want confirmation_bypass_mismatch; run error = %s", got.GetCode(), protojson.Format(got))
	}
	if got.Hint != nil {
		t.Errorf("hint = %q, want none: a hint naming the subject would steer an unattended run into a destroy nobody confirmed", got.GetHint())
	}
}
