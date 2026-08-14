package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestMergeDeployEnv(t *testing.T) {
	t.Parallel()

	t.Run("gives every app the supplied keys as plain variables", func(t *testing.T) {
		t.Parallel()

		variables := map[string][]manifestbuilder.Variable{
			"web": {{Key: "POSTHOG_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "ph-123"}},
			"api": nil,
		}

		if err := mergeDeployEnv(variables, map[string]string{"B": "2", "A": "1"}); err != nil {
			t.Fatalf("mergeDeployEnv: %v", err)
		}

		wantAPI := []manifestbuilder.Variable{
			{Key: "A", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "1"},
			{Key: "B", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Value: "2"},
		}
		if !reflect.DeepEqual(variables["api"], wantAPI) {
			t.Errorf("api = %+v, want %+v", variables["api"], wantAPI)
		}
		if got := variables["web"]; len(got) != 3 || got[0].Key != "POSTHOG_ID" {
			t.Errorf("web = %+v, want the declared variable kept alongside the supplied ones", got)
		}
	})

	t.Run("leaves the variables alone when nothing is supplied", func(t *testing.T) {
		t.Parallel()

		variables := map[string][]manifestbuilder.Variable{"web": nil}
		if err := mergeDeployEnv(variables, nil); err != nil {
			t.Fatalf("mergeDeployEnv: %v", err)
		}
		if variables["web"] != nil {
			t.Errorf("web = %+v, want nothing added", variables["web"])
		}
	})

	t.Run("refuses a key the project already declares", func(t *testing.T) {
		t.Parallel()

		variables := map[string][]manifestbuilder.Variable{
			"web": {{Key: "STRIPE_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET}},
		}

		err := mergeDeployEnv(variables, map[string]string{"STRIPE_KEY": "sk-live"})
		if err == nil {
			t.Fatal("mergeDeployEnv succeeded; a declared key must not be shadowed silently")
		}
		if !strings.Contains(err.Error(), "STRIPE_KEY") {
			t.Errorf("error = %q, want it to name STRIPE_KEY", err)
		}
	})
}

func TestDeployEnvByApp(t *testing.T) {
	t.Parallel()

	t.Run("keys the root app the way the builder expects", func(t *testing.T) {
		t.Parallel()

		got := deployEnvByApp(&projectconfig.Config{}, map[string]string{"A": "1"})
		want := map[string]map[string]string{"": {"A": "1"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("deployEnvByApp = %v, want %v", got, want)
		}
	})

	t.Run("hands each configured app the same env", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}, {Name: "api"}}}
		got := deployEnvByApp(cfg, map[string]string{"A": "1"})
		want := map[string]map[string]string{"web": {"A": "1"}, "api": {"A": "1"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("deployEnvByApp = %v, want %v", got, want)
		}
	})

	t.Run("asks for no build env when nothing is supplied", func(t *testing.T) {
		t.Parallel()

		if got := deployEnvByApp(&projectconfig.Config{}, nil); got != nil {
			t.Errorf("deployEnvByApp = %v, want nil", got)
		}
	})
}
