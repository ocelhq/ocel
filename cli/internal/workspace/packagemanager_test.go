package workspace_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/workspace"
)

func TestEachPackageManagerRunsTheAppsOwnScriptsRatherThanTheRoots(t *testing.T) {
	for _, tt := range []struct {
		name     string
		location workspace.Location
		want     workspace.Commands
	}{
		{
			name:     "pnpm scopes the install and the build to the app and what it depends on",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.Pnpm, App: workspace.App{Name: "@acme/web", Build: true, Start: true}},
			want: workspace.Commands{
				Install: "pnpm install --frozen-lockfile --filter \"{./apps/web}...\"",
				Build:   "pnpm --filter \"{./apps/web}...\" run build",
				Start:   "pnpm --filter \"{./apps/web}\" run start",
			},
		},
		{
			name:     "an app with no build script is not handed the root's",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.Pnpm, App: workspace.App{Name: "@acme/web", Start: true}},
			want: workspace.Commands{
				Install: "pnpm install --frozen-lockfile --filter \"{./apps/web}...\"",
				Start:   "pnpm --filter \"{./apps/web}\" run start",
			},
		},
		{
			name:     "an app with no start script starts the entry its manifest names, from its own directory",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.Pnpm, App: workspace.App{Name: "@acme/web", Main: "dist/server.js"}},
			want: workspace.Commands{
				Install: "pnpm install --frozen-lockfile --filter \"{./apps/web}...\"",
				Start:   "cd apps/web && node dist/server.js",
			},
		},
		{
			name:     "an app with neither a start script nor a main falls back to the index beside it",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.Pnpm, App: workspace.App{Name: "@acme/web", Index: "index.js"}},
			want: workspace.Commands{
				Install: "pnpm install --frozen-lockfile --filter \"{./apps/web}...\"",
				Start:   "cd apps/web && node index.js",
			},
		},
		{
			name:     "npm addresses a workspace by its path",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.Npm, App: workspace.App{Name: "@acme/web", Build: true, Start: true}},
			want: workspace.Commands{
				Build: "npm run build -w apps/web",
				Start: "npm run start -w apps/web",
			},
		},
		{
			name:     "yarn berry installs only what the app reaches and builds its dependencies first",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.YarnBerry, App: workspace.App{Name: "@acme/web", Build: true, Start: true}},
			want: workspace.Commands{
				Install: "yarn workspaces focus @acme/web",
				Build:   "yarn workspaces foreach -R -t --from @acme/web run build",
				Start:   "yarn workspace @acme/web run start",
			},
		},
		{
			name:     "yarn classic addresses one workspace at a time",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.YarnClassic, App: workspace.App{Name: "@acme/web", Build: true, Start: true}},
			want: workspace.Commands{
				Build: "yarn workspace @acme/web run build",
				Start: "yarn workspace @acme/web run start",
			},
		},
		{
			name:     "bun filters the build by name and starts from the app's directory",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.Bun, App: workspace.App{Name: "@acme/web", Build: true, Start: true}},
			want: workspace.Commands{
				Build: "bun run --filter @acme/web build",
				Start: "cd apps/web && bun run start",
			},
		},
		{
			name:     "a nameless app is still addressed by the directory it sits in",
			location: workspace.Location{Path: "apps/web", Member: true, Manager: workspace.YarnClassic, App: workspace.App{Build: true, Start: true}},
			want: workspace.Commands{
				Build: "cd apps/web && yarn run build",
				Start: "cd apps/web && yarn run start",
			},
		},
		{
			name:     "an app that is its own root is left to railpack",
			location: workspace.Location{Path: ".", Manager: workspace.Pnpm, App: workspace.App{Name: "solo", Build: true, Start: true}},
			want:     workspace.Commands{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.location.Commands()
			if err != nil {
				t.Fatalf("Commands() = %v", err)
			}
			if got != tt.want {
				t.Errorf("Commands() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAWorkspaceAppWithNothingToStartIsRefusedRatherThanHandedTheRootsStart(t *testing.T) {
	location := workspace.Location{
		Root:    "/repo",
		Path:    "apps/web",
		Member:  true,
		Manager: workspace.Pnpm,
		App:     workspace.App{Name: "@acme/web", Build: true},
	}

	_, err := location.Commands()
	if err == nil {
		t.Fatal("Commands() derived a start command for an app that declares none, so the image would run the workspace root's start script")
	}
	for _, want := range []string{"@acme/web", "start", "apps/web/package.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Commands() = %v, and the reader is never told about %q", err, want)
		}
	}
}

func TestAnAppThatIsItsOwnRootWithNothingToStartIsLeftToRailpack(t *testing.T) {
	location := workspace.Location{Root: "/repo/app", Path: ".", Manager: workspace.Npm, App: workspace.App{Name: "solo"}}

	if _, err := location.Commands(); err != nil {
		t.Errorf("Commands() = %v, and railpack derives the start command of an app that is its own root", err)
	}
}
