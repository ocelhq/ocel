package providerv1

import (
	"strings"
	"testing"
)

func TestCheckClass_Matches(t *testing.T) {
	cases := []struct{ infra, required Environment_Class }{
		{Environment_CLASS_PREVIEW, Environment_CLASS_PREVIEW},
		{Environment_CLASS_PRODUCTION, Environment_CLASS_PRODUCTION},
	}
	for _, c := range cases {
		if err := CheckClass(c.infra, c.required); err != nil {
			t.Errorf("CheckClass(%v, %v) = %v, want nil", c.infra, c.required, err)
		}
	}
}

func TestCheckClass_Mismatches(t *testing.T) {
	cases := []struct{ infra, required Environment_Class }{
		{Environment_CLASS_PRODUCTION, Environment_CLASS_PREVIEW},
		{Environment_CLASS_PREVIEW, Environment_CLASS_PRODUCTION},
		{Environment_CLASS_DEVELOPMENT, Environment_CLASS_PREVIEW},
		{Environment_CLASS_DEVELOPMENT, Environment_CLASS_PRODUCTION},
		{Environment_CLASS_UNSPECIFIED, Environment_CLASS_PREVIEW},
		{Environment_CLASS_UNSPECIFIED, Environment_CLASS_PRODUCTION},
	}
	for _, c := range cases {
		err := CheckClass(c.infra, c.required)
		if err == nil {
			t.Errorf("CheckClass(%v, %v) = nil, want error", c.infra, c.required)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), "substrate") {
			t.Errorf("CheckClass(%v, %v) error uses abstract word 'substrate': %q", c.infra, c.required, err)
		}
	}
}

func TestCheckClass_ErrorNamesTheCommandAndInfra(t *testing.T) {
	err := CheckClass(Environment_CLASS_PRODUCTION, Environment_CLASS_PREVIEW)
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

	err = CheckClass(Environment_CLASS_PREVIEW, Environment_CLASS_PRODUCTION)
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
