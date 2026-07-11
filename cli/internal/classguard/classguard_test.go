package classguard

import (
	"strings"
	"testing"
)

func TestCheck_Matches(t *testing.T) {
	cases := []struct{ infra, required string }{
		{ClassPreview, ClassPreview},
		{ClassProduction, ClassProduction},
	}
	for _, c := range cases {
		if err := Check(c.infra, c.required); err != nil {
			t.Errorf("Check(%q, %q) = %v, want nil", c.infra, c.required, err)
		}
	}
}

func TestCheck_Mismatches(t *testing.T) {
	cases := []struct{ infra, required string }{
		{ClassProduction, ClassPreview},
		{ClassPreview, ClassProduction},
		{ClassDevelopment, ClassPreview},
		{ClassDevelopment, ClassProduction},
		{"", ClassPreview},
		{"", ClassProduction},
	}
	for _, c := range cases {
		err := Check(c.infra, c.required)
		if err == nil {
			t.Errorf("Check(%q, %q) = nil, want error", c.infra, c.required)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), "substrate") {
			t.Errorf("Check(%q, %q) error uses abstract word 'substrate': %q", c.infra, c.required, err)
		}
	}
}

func TestCheck_ErrorNamesTheCommandAndInfra(t *testing.T) {
	err := Check(ClassProduction, ClassPreview)
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

	err = Check(ClassPreview, ClassProduction)
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
