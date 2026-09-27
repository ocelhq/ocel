package consent_test

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/consent"
)

func TestABypassNamingAnotherSubjectOnATerminalFallsBackToConfirmingAndSaysWhy(t *testing.T) {
	t.Setenv(consent.BypassEnv, "shop")

	granted, notice, err := consent.Bypass{Noun: "project", Subject: "acme", Action: "destroying production", Verb: "destroyed", TTY: true}.Granted()

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
