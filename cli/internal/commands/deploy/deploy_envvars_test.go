package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func definition(key string, class resourcesv1.VariableClass) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{Key: key, Class: class}
}

func TestAppVariables(t *testing.T) {
	t.Parallel()

	t.Run("pairs each declaration with what was resolved for it", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("POSTHOG_ID", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			definition("WEBHOOK_SECRET", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"POSTHOG_ID":     {Value: "ph-123"},
			"WEBHOOK_SECRET": {},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "POSTHOG_ID" || got[0].Value != "ph-123" ||
			got[0].Class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			t.Errorf("POSTHOG_ID = %+v, want its class and its resolved value", got[0])
		}
		if got[1].Value != "" {
			t.Errorf("WEBHOOK_SECRET = %+v, want no value: a live value never reaches a build host", got[1])
		}
	})

	t.Run("omits a key this app cannot read", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("CHECKOUT_ONLY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
		}

		if got := appVariables(definitions, map[string]variables.ResolvedValue{}); len(got) != 0 {
			t.Fatalf("appVariables = %+v, want nothing for a key this app resolves no cell for", got)
		}
	})

	t.Run("keeps client accessibility from the declaration", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			{Key: "PUBLIC_SITE_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, ClientAccessible: true},
			{Key: "INTERNAL_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN},
		}
		resolved := map[string]variables.ResolvedValue{
			"PUBLIC_SITE_URL": {Value: "https://example.com"},
			"INTERNAL_URL":    {Value: "http://internal"},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if !got[0].ClientAccessible {
			t.Errorf("PUBLIC_SITE_URL = %+v, want it marked client-accessible", got[0])
		}
		if got[1].ClientAccessible {
			t.Errorf("INTERNAL_URL = %+v, want it left server-only", got[1])
		}
	})

	t.Run("keeps the version each value resolved at", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("PLAIN_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
			definition("LIVE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"PLAIN_KEY": {Value: "v", Version: 2},
			"LIVE_KEY":  {Version: 9},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "PLAIN_KEY" || got[0].Version != 2 {
			t.Errorf("PLAIN_KEY = %+v, want the version its cell resolved at", got[0])
		}
		if got[1].Key != "LIVE_KEY" || got[1].Version != 9 {
			t.Errorf("LIVE_KEY = %+v, want its cell's version included too", got[1])
		}
	})

	t.Run("keeps the folder each key resolved from", func(t *testing.T) {
		t.Parallel()

		definitions := []*resourcesv1.VariableDefinition{
			definition("ROOT_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
			definition("SCOPED_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET),
		}
		resolved := map[string]variables.ResolvedValue{
			"ROOT_KEY":   {},
			"SCOPED_KEY": {Folder: "/admin"},
		}

		got := appVariables(definitions, resolved)
		if len(got) != 2 {
			t.Fatalf("appVariables = %+v, want both declarations", got)
		}
		if got[0].Key != "ROOT_KEY" || got[0].Folder != "" {
			t.Errorf("ROOT_KEY = %+v, want the empty root spelling, never the store's %q sentinel", got[0], "/")
		}
		if got[1].Key != "SCOPED_KEY" || got[1].Folder != "/admin" {
			t.Errorf("SCOPED_KEY = %+v, want the folder it resolved from", got[1])
		}
	})
}
