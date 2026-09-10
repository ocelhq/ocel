package envgate_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func member(key, group string, required bool) *resourcesv1.VariableDefinition {
	return &resourcesv1.VariableDefinition{
		Key:      key,
		Class:    resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
		Required: required,
		Group:    group,
	}
}

func groupOf(key string, required bool, description string) *resourcesv1.GroupDefinition {
	return &resourcesv1.GroupDefinition{Key: key, Required: required, Description: description}
}

func declareGrouped(t *testing.T, g *envgate.Gate, groups []*resourcesv1.GroupDefinition, definitions ...*resourcesv1.VariableDefinition) error {
	t.Helper()
	_, err := g.DeclareEnv(context.Background(), &resourcesv1.DeclareEnvRequest{Definitions: definitions, Groups: groups})
	return err
}

func owed(t *testing.T, g *envgate.Gate, appName string) []string {
	t.Helper()
	var keys []string
	for _, cell := range app(t, g.Matrix(nil), appName).Missing {
		keys = append(keys, cell.Key)
	}
	return keys
}

func TestDeclareEnvRefusesMalformedGroups(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		groups      []*resourcesv1.GroupDefinition
		definitions []*resourcesv1.VariableDefinition
		want        string
	}{
		{
			name:        "a group without a key",
			groups:      []*resourcesv1.GroupDefinition{groupOf("", false, "")},
			definitions: []*resourcesv1.VariableDefinition{member("GITHUB_CLIENT_ID", "", true)},
			want:        "key",
		},
		{
			name:        "one key declared by two groups",
			groups:      []*resourcesv1.GroupDefinition{groupOf("github", false, ""), groupOf("github", true, "")},
			definitions: []*resourcesv1.VariableDefinition{member("GITHUB_CLIENT_ID", "github", true)},
			want:        "declared twice",
		},
		{
			name:        "a member naming a group nothing declares",
			definitions: []*resourcesv1.VariableDefinition{member("GITHUB_CLIENT_ID", "github", true)},
			want:        "not declared",
		},
		{
			name:   "a group nothing belongs to",
			groups: []*resourcesv1.GroupDefinition{groupOf("github", false, "")},
			want:   "no members",
		},
		{
			name:   "two groups nothing belongs to name the first declared",
			groups: []*resourcesv1.GroupDefinition{groupOf("github", false, ""), groupOf("stripe", false, "")},
			want:   "environment group github has no members",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := prefetched(t, newFakeValues())

			err := declareGrouped(t, g, tc.groups, tc.definitions...)
			if err == nil {
				t.Fatalf("DeclareEnv err = nil, want %q refused", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestDeclareEnvRefusesOneGroupKeyClaimedTwice(t *testing.T) {
	t.Parallel()

	g := prefetched(t, newFakeValues())
	if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "")},
		member("GITHUB_CLIENT_ID", "github", true),
	); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}

	err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", true, "")},
		member("GITHUB_CLIENT_SECRET", "github", true),
	)
	if err == nil {
		t.Fatal("DeclareEnv err = nil, want one group key claimed by two files refused")
	}
	if !strings.Contains(err.Error(), "declared twice") {
		t.Errorf("err = %v, want it to say the group is declared twice", err)
	}
	if got := g.Groups(); len(got) != 1 {
		t.Errorf("groups = %+v, want the refused declaration left out of the gate", got)
	}
}

func TestGroupPresenceGatesWhatIsOwed(t *testing.T) {
	t.Parallel()

	t.Run("an optional group nothing has delivered owes nothing", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "")},
			member("GITHUB_CLIENT_ID", "github", true),
			member("GITHUB_CLIENT_SECRET", "github", true),
		); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
		if got := owed(t, g, "web"); len(got) != 0 {
			t.Errorf("owed = %v, want nothing: an optional group with no member set is off", got)
		}
	})

	t.Run("one delivered member turns an optional group on, and the rest come due", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("GITHUB_CLIENT_ID", "", "id")
		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "")},
			member("GITHUB_CLIENT_ID", "github", true),
			member("GITHUB_CLIENT_SECRET", "github", true),
		); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
		if got := owed(t, g, "web"); !reflect.DeepEqual(got, []string{"GITHUB_CLIENT_SECRET"}) {
			t.Errorf("owed = %v, want [GITHUB_CLIENT_SECRET]", got)
		}
	})

	t.Run("a required group owes its required members with nothing set", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", true, "")},
			member("GITHUB_CLIENT_ID", "github", true),
			member("GITHUB_CLIENT_SECRET", "github", true),
		); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
		if got := owed(t, g, "web"); !reflect.DeepEqual(got, []string{"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET"}) {
			t.Errorf("owed = %v, want both members", got)
		}
	})

	t.Run("a member spelled optional is never owed, however the group stands", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", true, "")},
			member("GITHUB_CLIENT_ID", "github", true),
			member("GITHUB_SCOPES", "github", false),
		); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
		if got := owed(t, g, "web"); !reflect.DeepEqual(got, []string{"GITHUB_CLIENT_ID"}) {
			t.Errorf("owed = %v, want the optional member left out", got)
		}
	})

	t.Run("a group that is on owes only the members spelled required", func(t *testing.T) {
		t.Parallel()
		values := newFakeValues()
		values.set("GITHUB_ENABLED", "", "1")
		g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "")},
			member("GITHUB_ENABLED", "github", true),
			member("GITHUB_CLIENT_ID", "github", true),
			member("GITHUB_SCOPES", "github", false),
		); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
		if got := owed(t, g, "web"); !reflect.DeepEqual(got, []string{"GITHUB_CLIENT_ID"}) {
			t.Errorf("owed = %v, want [GITHUB_CLIENT_ID]", got)
		}
	})

	t.Run("a group of members all spelled optional never blocks", func(t *testing.T) {
		t.Parallel()
		g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
		if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", true, "")},
			member("GITHUB_CLIENT_ID", "github", false),
			member("GITHUB_SCOPES", "github", false),
		); err != nil {
			t.Fatalf("DeclareEnv: %v", err)
		}
		if got := owed(t, g, "web"); len(got) != 0 {
			t.Errorf("owed = %v, want nothing", got)
		}
	})
}

func TestGroupPresenceFollowsFolderInheritance(t *testing.T) {
	t.Parallel()

	values := newFakeValues()
	values.set("GITHUB_CLIENT_ID", "", "id")
	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web", Folder: "/web"}}})
	if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "")},
		member("GITHUB_CLIENT_ID", "github", true),
		member("GITHUB_CLIENT_SECRET", "github", true),
	); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}

	if got := owed(t, g, "web"); !reflect.DeepEqual(got, []string{"GITHUB_CLIENT_SECRET"}) {
		t.Errorf("owed = %v, want the root value to turn the group on for /web and only the unset member owed", got)
	}
}

func TestStandings(t *testing.T) {
	t.Parallel()

	groups := []*resourcesv1.GroupDefinition{groupOf("github", false, "Sign in with GitHub"), groupOf("stripe", true, "")}
	definitions := []*resourcesv1.VariableDefinition{
		member("GITHUB_CLIENT_ID", "github", true),
		member("GITHUB_CLIENT_SECRET", "github", true),
		member("STRIPE_KEY", "stripe", true),
		def("LOG_LEVEL", resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN),
	}

	t.Run("the groups come back in declaration order", func(t *testing.T) {
		t.Parallel()
		standings := envgate.Standings(definitions, groups, nil, "")
		if len(standings) != 2 {
			t.Fatalf("standings = %+v, want one per group", standings)
		}
		if standings[0].Key != "github" || standings[1].Key != "stripe" {
			t.Errorf("standings = %+v, want declaration order", standings)
		}
	})

	t.Run("nothing set leaves an optional group owing nothing and a required one owing", func(t *testing.T) {
		t.Parallel()
		standings := envgate.Standings(definitions, groups, nil, "")
		if len(standings[0].Set) != 0 || len(standings[0].Missing) != 0 {
			t.Errorf("github = %+v, want an off group owing nothing", standings[0])
		}
		if !reflect.DeepEqual(standings[1].Missing, []string{"STRIPE_KEY"}) {
			t.Errorf("stripe missing = %v, want [STRIPE_KEY]: a required group owes its members whatever is set", standings[1].Missing)
		}
	})

	t.Run("one member set leaves the rest owed, in declaration order", func(t *testing.T) {
		t.Parallel()
		standings := envgate.Standings(definitions, groups, []envgate.Cell{{Key: "GITHUB_CLIENT_ID"}}, "")
		if !reflect.DeepEqual(standings[0].Set, []string{"GITHUB_CLIENT_ID"}) || !reflect.DeepEqual(standings[0].Missing, []string{"GITHUB_CLIENT_SECRET"}) {
			t.Errorf("github = %+v, want the set and the owed named", standings[0])
		}
	})

	t.Run("a member spelled optional is neither set nor owed until it has a value", func(t *testing.T) {
		t.Parallel()
		optional := append(slices.Clone(definitions), member("GITHUB_SCOPES", "github", false))
		standings := envgate.Standings(optional, groups, []envgate.Cell{{Key: "GITHUB_CLIENT_ID"}}, "")
		if len(standings[0].Set)+len(standings[0].Missing) != 2 {
			t.Errorf("github = %+v, want the optional member out of the count: it is never owed", standings[0])
		}
	})

	t.Run("a value at the root completes a group read from a folder", func(t *testing.T) {
		t.Parallel()
		held := []envgate.Cell{{Key: "GITHUB_CLIENT_ID"}, {Key: "GITHUB_CLIENT_SECRET", Folder: "/web"}}
		standings := envgate.Standings(definitions, groups, held, "/web")
		if len(standings[0].Missing) != 0 {
			t.Errorf("github = %+v, want nothing owed: /web inherits the root value", standings[0])
		}
	})
}

func TestRefusalGathersGroupedMembersInDeclarationOrder(t *testing.T) {
	t.Parallel()

	values := newFakeValues()
	values.set("GITHUB_CLIENT_ID", "", "id")
	g := prefetched(t, values, envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "Sign in with GitHub")},
		member("GITHUB_CLIENT_ID", "github", true),
		member("GITHUB_REDIRECT_URL", "github", true),
		member("GITHUB_CLIENT_SECRET", "github", true),
	); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}
	if err := declareGrouped(t, g, nil, def("DATABASE_URL", resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}

	err := g.Check()
	if err == nil {
		t.Fatal("Check err = nil, want a refusal")
	}
	want := strings.Join([]string{
		"✗ 3 variables are not ready — nothing has been built.",
		"",
		"  github — set together (Sign in with GitHub)",
		"    ✗ GITHUB_REDIRECT_URL   root  no value",
		"    ✗ GITHUB_CLIENT_SECRET  root  no value",
		"  ✗ DATABASE_URL            root  no value",
		"",
		"  Fill them in: ocel env set <KEY>=<VALUE>",
	}, "\n")
	if err.Error() != want {
		t.Errorf("refusal =\n%s\nwant\n%s", err.Error(), want)
	}
}

func TestMatrixCarriesGroupsOnce(t *testing.T) {
	t.Parallel()

	g := prefetched(t, newFakeValues(), envgate.Scope{Apps: []envgate.App{{Name: "web"}}})
	if err := declareGrouped(t, g, []*resourcesv1.GroupDefinition{groupOf("github", false, "Sign in with GitHub"), groupOf("stripe", true, "")},
		member("GITHUB_CLIENT_ID", "github", true),
		member("STRIPE_KEY", "stripe", true),
	); err != nil {
		t.Fatalf("DeclareEnv: %v", err)
	}

	m := g.Matrix(nil)
	want := []envgate.MatrixGroup{
		{Key: "github", Description: "Sign in with GitHub"},
		{Key: "stripe", Required: true},
	}
	if !reflect.DeepEqual(m.Groups, want) {
		t.Errorf("groups = %+v, want %+v in declaration order", m.Groups, want)
	}
	if got := row(t, m, "GITHUB_CLIENT_ID").Group; got != "github" {
		t.Errorf("row group = %q, want the group key", got)
	}

	doc, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"groupOptional", "groupDescription"} {
		if strings.Contains(string(doc), gone) {
			t.Errorf("matrix json = %s, want %q off the rows: a group states its facts once", doc, gone)
		}
	}
}
