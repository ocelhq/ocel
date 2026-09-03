package envgate_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
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
				"  Fill them in: ocel env set STRIPE_API_KEY <VALUE>",
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
				"  Fill them in: ocel env set <KEY> <VALUE> --folder <FOLDER>",
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
				"  Fill them in: ocel env set <KEY> <VALUE> --folder <FOLDER> --preview",
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
				"  Fill them in: ocel env set STRIPE_KEY <VALUE> --folder /web --preview",
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
			name:     "the editor remedy carries the preview flag",
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

func TestRefusalOwedIsTheStreamFormOfError(t *testing.T) {
	t.Parallel()
	refusal := &envgate.Refusal{
		Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), invalid("PORT", "/web", "not a number")},
		Scope:    envgate.Scope{Browser: true},
	}
	owed := refusal.Owed()
	if len(owed.GetCells()) != 2 {
		t.Fatalf("Owed().Cells = %+v, want one per problem", owed.GetCells())
	}
	if got := owed.GetCells()[1]; got.GetKey() != "PORT" || got.GetFolder() != "/web" || got.GetReason() != "set, but not a number" {
		t.Errorf("Owed().Cells[1] = %+v, want the key, folder and reason of the invalid cell", got)
	}
	if owed.GetRemedy() != "ocel env ui" {
		t.Errorf("Owed().Remedy = %q, want the editor", owed.GetRemedy())
	}
	plain := strings.Join(append(envgate.Lines(owed.GetCells(), envgate.Plain), "", envgate.RemedyLine(owed.GetRemedy())), "\n")
	if plain != refusal.Error() {
		t.Errorf("Lines(Owed()) =\n%s\nwant Error()\n%s", plain, refusal.Error())
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
	got := envgate.Lines(refusal.Owed().GetCells(), paint)
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
	if plain := strings.Join(envgate.Lines(refusal.Owed().GetCells(), envgate.Plain), "\n"); stripped != plain {
		t.Errorf("painted minus codes =\n%s\nwant the plain form\n%s", stripped, plain)
	}
}
