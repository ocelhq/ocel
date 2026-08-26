package deploy

import (
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/envwire"
)

func writeAppNeeds(t *testing.T, root, app, framework, needs string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, ".ocel", "output", "apps", app, edge.ServeDescriptorFile),
		`{"framework":"`+framework+`","buildId":"b1","needs":`+needs+`}`)
}

func lintEdgeWarnings(t *testing.T, cfg *projectconfig.Config) []string {
	t.Helper()
	definition := &resourcesv1.VariableDefinition{
		Key:    "STRIPE_KEY",
		Class:  resourcesv1.VariableClass_VARIABLE_CLASS_SECRET,
		Source: "env.ts",
	}
	warnings, err := envgate.LintEdge(
		[]*resourcesv1.VariableDefinition{definition},
		envwire.Apps(cfg),
		edgeApps(cfg),
	)
	if err != nil {
		t.Fatalf("LintEdge: %v", err)
	}
	return warnings
}

func TestEdgeAppsReadsTheNeeds(t *testing.T) {
	t.Parallel()

	t.Run("a need for edge code names the app and warns about the secret", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{Dir: t.TempDir()}
		writeAppNeeds(t, cfg.Dir, "web", "next", `{"edge-runtime":{"count":1,"routes":["/edgy"]}}`)

		if apps := edgeApps(cfg); len(apps) != 1 || apps[0] != envwire.RootApp {
			t.Fatalf("edgeApps = %v, want the project's sole app", apps)
		}
		if warnings := lintEdgeWarnings(t, cfg); len(warnings) != 1 {
			t.Fatalf("warnings = %q, want the secret warning", warnings)
		}
	})

	t.Run("a node app needs nothing and lints clean", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{Dir: t.TempDir()}
		writeAppNeeds(t, cfg.Dir, "api", "express", `{}`)

		if apps := edgeApps(cfg); len(apps) != 0 {
			t.Fatalf("edgeApps = %v, want none", apps)
		}
		if warnings := lintEdgeWarnings(t, cfg); len(warnings) != 0 {
			t.Fatalf("warnings = %q, want none", warnings)
		}
	})

	t.Run("needs that ship no customer code name no edge app", func(t *testing.T) {
		t.Parallel()

		cfg := &projectconfig.Config{Dir: t.TempDir()}
		writeAppNeeds(t, cfg.Dir, "web", "next",
			`{"edge-cache":{"count":3},"streaming":{"count":2},"ppr-resume":{"count":1,"routes":["/"]}}`)

		if apps := edgeApps(cfg); len(apps) != 0 {
			t.Fatalf("edgeApps = %v, want none", apps)
		}
	})
}
