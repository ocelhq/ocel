package envgate_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	"google.golang.org/protobuf/proto"
)

func missing(key, folder string) *resourcesv1.VariableProblem {
	return &resourcesv1.VariableProblem{Key: key, Folder: folder, Kind: resourcesv1.VariableProblem_KIND_MISSING}
}

func invalid(key, folder, detail string) *resourcesv1.VariableProblem {
	return &resourcesv1.VariableProblem{Key: key, Folder: folder, Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: detail}
}

func TestRefusalIsOneLinePerCell(t *testing.T) {
	t.Parallel()

	apps := []envgate.App{{Name: "web", Folder: "/web"}, {Name: "api"}}
	for _, tc := range []struct {
		name     string
		problems []*resourcesv1.VariableProblem
		scope    envgate.Scope
		want     string
	}{
		{
			name:     "one missing cell",
			problems: []*resourcesv1.VariableProblem{missing("STRIPE_API_KEY", "")},
			scope:    envgate.Scope{Apps: apps},
			want: strings.Join([]string{
				"✗ 1 variable is not ready — nothing has been built.",
				"",
				"  ✗ STRIPE_API_KEY  root  no value",
				"",
				"  Fill them in: ocel env set STRIPE_API_KEY=<VALUE>",
			}, "\n"),
		},
		{
			name: "many cells align into columns",
			problems: []*resourcesv1.VariableProblem{
				missing("DATABASE_URL", ""),
				missing("STRIPE_KEY", "/web"),
				missing("SENTRY_DSN", "/services/api"),
			},
			scope: envgate.Scope{Apps: apps},
			want: strings.Join([]string{
				"✗ 3 variables are not ready — nothing has been built.",
				"",
				"  ✗ DATABASE_URL  root           no value",
				"  ✗ STRIPE_KEY    /web           no value",
				"  ✗ SENTRY_DSN    /services/api  no value",
				"",
				"  Fill them in: ocel env set <KEY>=<VALUE> --folder <FOLDER>",
			}, "\n"),
		},
		{
			name: "missing and invalid share one shape",
			problems: []*resourcesv1.VariableProblem{
				missing("DATABASE_URL", ""),
				invalid("PORT", "", "not a number"),
				invalid("API_BASE", "/web", ""),
			},
			scope: envgate.Scope{Apps: apps, Preview: true},
			want: strings.Join([]string{
				"✗ 3 variables are not ready — nothing has been built.",
				"",
				"  ✗ DATABASE_URL  root  no value",
				"  ✗ PORT          root  set, but not a number",
				"  ✗ API_BASE      /web  set, but it does not satisfy its schema",
				"",
				"  Fill them in: ocel env set <KEY>=<VALUE> --folder <FOLDER> --preview",
			}, "\n"),
		},
		{
			name:     "one cell in a folder on a preview",
			problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "/web")},
			scope:    envgate.Scope{Apps: apps, Preview: true},
			want: strings.Join([]string{
				"✗ 1 variable is not ready — nothing has been built.",
				"",
				"  ✗ STRIPE_KEY  /web  no value",
				"",
				"  Fill them in: ocel env set STRIPE_KEY=<VALUE> --folder /web --preview",
			}, "\n"),
		},
		{
			name:     "a named preview environment is named in the command",
			problems: []*resourcesv1.VariableProblem{missing("STRIPE_KEY", "")},
			scope:    envgate.Scope{Apps: apps, Preview: true, Environment: "pr-12"},
			want: strings.Join([]string{
				"✗ 1 variable is not ready — nothing has been built.",
				"",
				"  ✗ STRIPE_KEY  root  no value",
				"",
				"  Fill them in: ocel env set STRIPE_KEY=<VALUE> --preview --environment pr-12",
			}, "\n"),
		},
		{
			name:     "a reachable browser is sent to the editor",
			problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), missing("STRIPE_KEY", "/web")},
			scope:    envgate.Scope{Apps: apps, Browser: true},
			want: strings.Join([]string{
				"✗ 2 variables are not ready — nothing has been built.",
				"",
				"  ✗ DATABASE_URL  root  no value",
				"  ✗ STRIPE_KEY    /web  no value",
				"",
				"  Fill them in: ocel env ui",
			}, "\n"),
		},
		{
			name:     "the editor remedy includes the preview flag",
			problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", "")},
			scope:    envgate.Scope{Apps: apps, Browser: true, Preview: true},
			want: strings.Join([]string{
				"✗ 1 variable is not ready — nothing has been built.",
				"",
				"  ✗ DATABASE_URL  root  no value",
				"",
				"  Fill them in: ocel env ui --preview",
			}, "\n"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			refusal := &envgate.Refusal{Problems: tc.problems, Scope: tc.scope}
			if got := refusal.Error(); got != tc.want {
				t.Errorf("Error() =\n%s\nwant\n%s", got, tc.want)
			}
			for _, absent := range []string{"read by", "fix:", "run this command again"} {
				if strings.Contains(refusal.Error(), absent) {
					t.Errorf("Error() = %q, want no %q", refusal.Error(), absent)
				}
			}
			if n := strings.Count(refusal.Error(), "Fill them in"); n != 1 {
				t.Errorf("Error() names the remedy %d times, want exactly once", n)
			}
		})
	}
}

func TestRefusalMissingIsTheStreamFormOfError(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), invalid("PORT", "/web", "not a number")},
		Scope:    envgate.Scope{Browser: true},
	}
	missing := refusal.Missing()
	if len(missing.GetCells()) != 2 {
		t.Fatalf("Missing().Cells = %+v, want one per problem", missing.GetCells())
	}
	if got := missing.GetCells()[1]; got.GetKey() != "PORT" || got.GetFolder() != "/web" || got.GetReason() != "set, but not a number" {
		t.Errorf("Missing().Cells[1] = %+v, want the key, folder and reason of the invalid cell", got)
	}
	if missing.GetRemedy() != "ocel env ui" {
		t.Errorf("Missing().Remedy = %q, want the editor", missing.GetRemedy())
	}
	plain := strings.Join(append(envgate.Lines(missing, envgate.Plain), "", envgate.RemedyLine(missing.GetRemedy())), "\n")
	if plain != refusal.Error() {
		t.Errorf("Lines(Missing()) =\n%s\nwant Error()\n%s", plain, refusal.Error())
	}
}

func TestRefusalPrintsTheVariableDescription(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("STRIPE_API_KEY", "")},
		Definitions: []*resourcesv1.VariableDefinition{{
			Key:         "STRIPE_API_KEY",
			Description: "Used to call Stripe",
		}},
	}
	if got := refusal.Error(); !strings.Contains(got, "Used to call Stripe") {
		t.Errorf("Error() = %q, want the variable description", got)
	}
	if got := refusal.Missing().GetCells()[0].GetDescription(); got != "Used to call Stripe" {
		t.Errorf("Missing().Cells[0].Description = %q, want %q", got, "Used to call Stripe")
	}
}

func TestMissingSendsGroupingOverTheWire(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{
			missing("GITHUB_CLIENT_ID", ""),
			missing("GITHUB_CLIENT_SECRET", ""),
			missing("DATABASE_URL", ""),
		},
		Definitions: []*resourcesv1.VariableDefinition{
			{Key: "GITHUB_CLIENT_ID", Group: "github", Required: true},
			{Key: "GITHUB_CLIENT_SECRET", Group: "github", Required: true},
			{Key: "DATABASE_URL", Required: true},
		},
		Groups: []*resourcesv1.GroupDefinition{{Key: "github", Description: "Sign in with GitHub"}},
	}

	encoded, err := proto.Marshal(refusal.Missing())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	wire := &streamv1.MissingVariables{}
	if err := proto.Unmarshal(encoded, wire); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got := strings.Join(append(envgate.Lines(wire, envgate.Plain), "", envgate.RemedyLine(wire.GetRemedy())), "\n")
	if got != refusal.Error() {
		t.Errorf("Lines(wire) =\n%s\nwant Error()\n%s", got, refusal.Error())
	}
	if !strings.Contains(got, "github — set together (Sign in with GitHub)") {
		t.Errorf("Lines(wire) =\n%s\nwant the group header", got)
	}
}

func TestPaintTouchesOnlyTheMarkAndTheFolder(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), invalid("PORT", "/web", "not a number")},
	}
	paint := envgate.Paint{
		Fail:  func(s string) string { return "<red>" + s + "</red>" },
		Faint: func(s string) string { return "<dim>" + s + "</dim>" },
	}
	got := envgate.Lines(refusal.Missing(), paint)
	want := []string{
		"<red>✗</red> 2 variables are not ready — nothing has been built.",
		"",
		"  <red>✗</red> DATABASE_URL  <dim>root</dim>  no value",
		"  <red>✗</red> PORT          <dim>/web</dim>  set, but not a number",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Lines() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	stripped := strings.NewReplacer("<red>", "", "</red>", "", "<dim>", "", "</dim>", "").Replace(strings.Join(got, "\n"))
	if plain := strings.Join(envgate.Lines(refusal.Missing(), envgate.Plain), "\n"); stripped != plain {
		t.Errorf("painted minus codes =\n%s\nwant the plain form\n%s", stripped, plain)
	}
}
