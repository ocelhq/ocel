package build

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestTheBuildEnvironmentHoldsEveryPlaintextValueUnderItsOwnNameAndNothingElse(t *testing.T) {
	built := SplitVariablesByClass([]clientenv.App{{Name: "storefront", Variables: []variables.Variable{
		{Key: "NEXT_PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "https://example.com", ClientAccessible: true},
		{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "http://internal"},
		{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
	}}}, nil)["storefront"]

	if got, want := built.Env["NEXT_PUBLIC_SITE_URL"], "https://example.com"; got != want {
		t.Errorf("NEXT_PUBLIC_SITE_URL = %q, want %q", got, want)
	}
	if got, want := built.Env["INTERNAL_URL"], "http://internal"; got != want {
		t.Errorf("INTERNAL_URL = %q, want %q: a build reads the plaintext class, client-accessible or not", got, want)
	}
	if _, ok := built.Env["STRIPE_API_KEY"]; ok {
		t.Error("env contains STRIPE_API_KEY; an encrypted class never enters the build's environment")
	}
}

func TestABuildReadsASensitiveValueFromItsLiveDir(t *testing.T) {
	built := SplitVariablesByClass([]clientenv.App{{Name: "storefront", Variables: []variables.Variable{
		{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, Value: "sk-live"},
		{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "http://internal"},
	}}}, nil)["storefront"]

	if got, want := built.Live["STRIPE_API_KEY"], "sk-live"; got != want {
		t.Errorf("live STRIPE_API_KEY = %q, want %q", got, want)
	}
	if _, ok := built.Live["INTERNAL_URL"]; ok {
		t.Error("live dir holds INTERNAL_URL; a plaintext value stays in the environment")
	}
}

func TestEachAppIsBuiltWithItsOwnValueForADivergedKey(t *testing.T) {
	built := SplitVariablesByClass([]clientenv.App{
		{Name: "storefront", Variables: []variables.Variable{{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-store"}}},
		{Name: "admin", Variables: []variables.Variable{{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-admin"}}},
	}, nil)

	if got, want := built["storefront"].Env["POSTHOG_ID"], "ph-store"; got != want {
		t.Errorf("storefront POSTHOG_ID = %q, want %q", got, want)
	}
	if got, want := built["admin"].Env["POSTHOG_ID"], "ph-admin"; got != want {
		t.Errorf("admin POSTHOG_ID = %q, want %q", got, want)
	}
}
