package variables_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func missing(key, folder string) *resourcesv1.VariableProblem {
	return &resourcesv1.VariableProblem{Key: key, Folder: folder, Kind: resourcesv1.VariableProblem_KIND_MISSING}
}

func invalid(key, folder, detail string) *resourcesv1.VariableProblem {
	return &resourcesv1.VariableProblem{Key: key, Folder: folder, Kind: resourcesv1.VariableProblem_KIND_INVALID, Detail: detail}
}

func TestAMissingErrorIsOneLinePerCell(t *testing.T) {
	t.Parallel()

	apps := []variables.App{{Name: "web", Folder: "/web"}, {Name: "api"}}
	for _, tc := range []struct {
		name     string
		problems []*resourcesv1.VariableProblem
		scope    variables.Scope
		want     string
	}{
		{
			name:     "one missing cell",
			problems: []*resourcesv1.VariableProblem{missing("STRIPE_API_KEY", "")},
			scope:    variables.Scope{Apps: apps},
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
			scope: variables.Scope{Apps: apps},
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
			scope: variables.Scope{Apps: apps, Tier: environmentv1.Tier_TIER_PREVIEW},
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
			scope:    variables.Scope{Apps: apps, Tier: environmentv1.Tier_TIER_PREVIEW},
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
			scope:    variables.Scope{Apps: apps, Tier: environmentv1.Tier_TIER_PREVIEW, Environment: "pr-12"},
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
			scope:    variables.Scope{Apps: apps, Browser: true},
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
			scope:    variables.Scope{Apps: apps, Browser: true, Tier: environmentv1.Tier_TIER_PREVIEW},
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
			refusal := &variables.MissingError{Problems: tc.problems, Scope: tc.scope}
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

func TestAMissingErrorsVariablesAreTheStreamFormOfItsMessage(t *testing.T) {
	t.Parallel()
	refusal := &variables.MissingError{
		Problems: []*resourcesv1.VariableProblem{missing("DATABASE_URL", ""), invalid("PORT", "/web", "not a number")},
		Scope:    variables.Scope{Browser: true},
	}
	missing := refusal.Variables()
	if len(missing.GetCells()) != 2 {
		t.Fatalf("Variables().Cells = %+v, want one per problem", missing.GetCells())
	}
	if got := missing.GetCells()[1]; got.GetKey() != "PORT" || got.GetFolder() != "/web" || got.GetReason() != "set, but not a number" {
		t.Errorf("Variables().Cells[1] = %+v, want the key, folder and reason of the invalid cell", got)
	}
	if missing.GetRemedy() != "ocel env ui" {
		t.Errorf("Variables().Remedy = %q, want the editor", missing.GetRemedy())
	}
	plain := strings.Join(append(terminal.MissingVariablesLines(missing, terminal.Presentation{}), "", terminal.MissingVariablesRemedy(missing.GetRemedy())), "\n")
	if plain != refusal.Error() {
		t.Errorf("MissingVariablesLines(Variables()) =\n%s\nwant Error()\n%s", plain, refusal.Error())
	}
}

func TestAMissingErrorPrintsTheVariableDescription(t *testing.T) {
	t.Parallel()
	refusal := &variables.MissingError{
		Problems: []*resourcesv1.VariableProblem{missing("STRIPE_API_KEY", "")},
		Definitions: []*resourcesv1.VariableDefinition{{
			Key:         "STRIPE_API_KEY",
			Description: "Used to call Stripe",
		}},
	}
	if got := refusal.Error(); !strings.Contains(got, "Used to call Stripe") {
		t.Errorf("Error() = %q, want the variable description", got)
	}
	if got := refusal.Variables().GetCells()[0].GetDescription(); got != "Used to call Stripe" {
		t.Errorf("Variables().Cells[0].Description = %q, want %q", got, "Used to call Stripe")
	}
}

func TestMissingVariablesCarryTheirGroupingOverTheWire(t *testing.T) {
	t.Parallel()
	refusal := &variables.MissingError{
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

	encoded, err := proto.Marshal(refusal.Variables())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	wire := &streamv1.MissingVariables{}
	if err := proto.Unmarshal(encoded, wire); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got := strings.Join(append(terminal.MissingVariablesLines(wire, terminal.Presentation{}), "", terminal.MissingVariablesRemedy(wire.GetRemedy())), "\n")
	if got != refusal.Error() {
		t.Errorf("MissingVariablesLines(wire) =\n%s\nwant Error()\n%s", got, refusal.Error())
	}
	if !strings.Contains(got, "github — set together (Sign in with GitHub)") {
		t.Errorf("MissingVariablesLines(wire) =\n%s\nwant the group header", got)
	}
}

func TestASchemaComplaintAboutAValueThatIsNotPlainQuotesNothingTheSDKSaid(t *testing.T) {
	t.Parallel()

	for _, class := range []resourcesv1.VariableClass{resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET} {
		t.Run(class.String(), func(t *testing.T) {
			t.Parallel()
			values := newFakeValues()
			values.set("STRIPE_KEY", "", "sk_live_leaked")
			g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "web"}}})
			declare(t, g, def("STRIPE_KEY", class))
			report(t, g, invalid("STRIPE_KEY", "", `expected "sk_test_", received "sk_live_leaked"`))

			err := g.RefuseIncomplete()
			if err == nil {
				t.Fatal("RefuseIncomplete() = nil, want the invalid value refused")
			}
			if strings.Contains(err.Error(), "sk_live_leaked") || !strings.Contains(err.Error(), "does not satisfy its schema") {
				t.Errorf("refusal = %q, want the generic reason and nothing the schema said", err)
			}
			problem := cell(t, row(t, g.Matrix(nil), "STRIPE_KEY"), "").Problem
			if strings.Contains(problem, "sk_live_leaked") || problem == "" {
				t.Errorf("matrix problem = %q, want a complaint that quotes nothing the schema said", problem)
			}
		})
	}

	t.Run("a complaint about a key not yet declared plain keeps nothing the SDK said", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), variables.Scope{Apps: []variables.App{{Name: "web"}}})
		report(t, g, invalid("UNDECLARED", "", "received hunter2"))
		declare(t, g, def("UNDECLARED", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))

		if err := g.RefuseIncomplete(); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("RefuseIncomplete() = %v, want a refusal quoting nothing the schema said", err)
		}
	})
}

func TestARunNeedingUnsetVariablesReportsVariablesMissingNamingTheKeysAndTheRemedy(t *testing.T) {
	t.Parallel()

	g := prefetched(t, newFakeValues(), variables.Scope{Apps: []variables.App{{Name: "web"}}})
	declare(t, g, def("STRIPE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET), def("DATABASE_URL", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE))
	err := g.RefuseIncomplete()

	got := clierror.NewRunError(fmt.Errorf("deploy: %w", fmt.Errorf("checking variables: %w", err)))
	if got.GetCode() != "variables.missing" {
		t.Fatalf("run error = %s, want variables.missing", protojson.Format(got))
	}
	for _, key := range []string{"STRIPE_KEY", "DATABASE_URL"} {
		if !strings.Contains(got.GetMessage(), key) {
			t.Errorf("message = %q, want it to name %s", got.GetMessage(), key)
		}
	}
	if got.GetHint() != "ocel env set <KEY>=<VALUE>" {
		t.Errorf("hint = %q, want the command that sets a variable", got.GetHint())
	}
	var refusal *variables.MissingError
	if !errors.As(err, &refusal) || len(refusal.Problems) != 2 {
		t.Errorf("err = %v, want the *MissingError still found through the code", err)
	}
}

func TestAnInvalidSecretReportsVariablesMissingWithoutItsValueInTheErrorObject(t *testing.T) {
	t.Parallel()

	values := newFakeValues()
	values.set("STRIPE_KEY", "", "sk_live_leaked")
	g := prefetched(t, values, variables.Scope{Apps: []variables.App{{Name: "web"}}})
	declare(t, g, def("STRIPE_KEY", resourcesv1.VariableClass_VARIABLE_CLASS_SECRET))
	report(t, g, invalid("STRIPE_KEY", "", `expected "sk_test_", received "sk_live_leaked"`))

	got := clierror.NewRunError(g.RefuseIncomplete())

	if got.GetCode() != "variables.missing" {
		t.Fatalf("run error = %s, want variables.missing", protojson.Format(got))
	}
	if strings.Contains(protojson.Format(got), "sk_live_leaked") {
		t.Errorf("run error = %s, want no value in it", protojson.Format(got))
	}
}

func TestMissingCredentialsReportVariablesMissing(t *testing.T) {
	t.Parallel()

	g := prefetched(t, newFakeValues(), variables.Scope{Apps: []variables.App{{Name: "web"}}})
	err := g.RefuseCredentials([]*resourcesv1.VariableProblem{missing("AWS_SECRET_ACCESS_KEY", "")})

	got := clierror.NewRunError(err)
	if got.GetCode() != "variables.missing" || !strings.Contains(got.GetMessage(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("run error = %s, want variables.missing naming the key", protojson.Format(got))
	}
}
