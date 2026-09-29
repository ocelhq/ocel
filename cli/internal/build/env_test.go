package build

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestTheBuildEnvironmentHoldsEveryPlaintextValueUnderItsOwnNameAndNothingElse(t *testing.T) {
	env := Env([]clientenv.App{{Name: "storefront", Variables: []manifestbuilder.Variable{
		{Key: "NEXT_PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://example.com", ClientAccessible: true},
		{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "http://internal"},
		{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
	}}})["storefront"]

	if got, want := env["NEXT_PUBLIC_SITE_URL"], "https://example.com"; got != want {
		t.Errorf("NEXT_PUBLIC_SITE_URL = %q, want %q", got, want)
	}
	if got, want := env["INTERNAL_URL"], "http://internal"; got != want {
		t.Errorf("INTERNAL_URL = %q, want %q: a build reads the plaintext class, client-accessible or not", got, want)
	}
	if _, ok := env["STRIPE_API_KEY"]; ok {
		t.Error("env contains STRIPE_API_KEY; an encrypted class is nothing a build may read")
	}
}

func TestEachAppIsBuiltWithItsOwnValueForADivergedKey(t *testing.T) {
	env := Env([]clientenv.App{
		{Name: "storefront", Variables: []manifestbuilder.Variable{{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"}}},
		{Name: "admin", Variables: []manifestbuilder.Variable{{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"}}},
	})

	if got, want := env["storefront"]["POSTHOG_ID"], "ph-store"; got != want {
		t.Errorf("storefront POSTHOG_ID = %q, want %q", got, want)
	}
	if got, want := env["admin"]["POSTHOG_ID"], "ph-admin"; got != want {
		t.Errorf("admin POSTHOG_ID = %q, want %q", got, want)
	}
}
