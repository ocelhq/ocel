package cli

import (
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestCheckClass_Matches(t *testing.T) {
	cases := []struct {
		infra, required deploymentsv1.Environment_Class
	}{
		{deploymentsv1.Environment_CLASS_PREVIEW, deploymentsv1.Environment_CLASS_PREVIEW},
		{deploymentsv1.Environment_CLASS_PRODUCTION, deploymentsv1.Environment_CLASS_PRODUCTION},
	}
	for _, c := range cases {
		if err := checkClass(c.infra, c.required); err != nil {
			t.Errorf("checkClass(%v, %v) = %v, want nil", c.infra, c.required, err)
		}
	}
}

func TestCheckClass_Mismatches(t *testing.T) {
	cases := []struct {
		infra, required deploymentsv1.Environment_Class
	}{
		{deploymentsv1.Environment_CLASS_PRODUCTION, deploymentsv1.Environment_CLASS_PREVIEW},
		{deploymentsv1.Environment_CLASS_PREVIEW, deploymentsv1.Environment_CLASS_PRODUCTION},
		{deploymentsv1.Environment_CLASS_UNSPECIFIED, deploymentsv1.Environment_CLASS_PREVIEW},
		{deploymentsv1.Environment_CLASS_UNSPECIFIED, deploymentsv1.Environment_CLASS_PRODUCTION},
	}
	for _, c := range cases {
		err := checkClass(c.infra, c.required)
		if err == nil {
			t.Errorf("checkClass(%v, %v) = nil, want error", c.infra, c.required)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), "substrate") {
			t.Errorf("checkClass(%v, %v) error uses abstract word 'substrate': %q", c.infra, c.required, err)
		}
	}
}

func TestCheckClass_ErrorNamesTheCommandAndInfra(t *testing.T) {
	err := checkClass(deploymentsv1.Environment_CLASS_PRODUCTION, deploymentsv1.Environment_CLASS_PREVIEW)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ocel preview") {
		t.Errorf("error should name the command, got %q", msg)
	}
	if !strings.Contains(msg, "preview infrastructure") {
		t.Errorf("error should name preview infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap --preview") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}

	err = checkClass(deploymentsv1.Environment_CLASS_PREVIEW, deploymentsv1.Environment_CLASS_PRODUCTION)
	if err == nil {
		t.Fatal("expected error")
	}
	msg = err.Error()
	if !strings.Contains(msg, "ocel deploy") {
		t.Errorf("error should name the command, got %q", msg)
	}
	if !strings.Contains(msg, "production infrastructure") {
		t.Errorf("error should name production infrastructure, got %q", msg)
	}
}
