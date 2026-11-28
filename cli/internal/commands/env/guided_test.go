package env

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/variables"
)

type guidingPrompt struct {
	answeringPrompt
	choices map[string]string
	offered map[string][]string
	confirm bool
	asked   []string
}

func (p *guidingPrompt) Select(_ context.Context, title string, options []prompt.Option) (string, bool, error) {
	if p.offered == nil {
		p.offered = map[string][]string{}
	}
	for _, option := range options {
		p.offered[title] = append(p.offered[title], option.Label)
	}
	chosen, ok := p.choices[title]
	return chosen, ok, nil
}

func (p *guidingPrompt) Confirm(_ context.Context, question string) (bool, error) {
	p.asked = append(p.asked, question)
	return p.confirm, nil
}

func required(folder string, set bool) variables.MatrixCell {
	return variables.MatrixCell{Folder: folder, State: variables.CellRequired, Set: set}
}

func forbidden(folder string) variables.MatrixCell {
	return variables.MatrixCell{Folder: folder, State: variables.CellForbidden}
}

func optional(folder string) variables.MatrixCell {
	return variables.MatrixCell{Folder: folder, State: variables.CellOptional}
}

func githubMatrix() variables.Matrix {
	return variables.Matrix{
		Columns: []string{"", "/web"},
		Rows: []variables.MatrixRow{
			{Key: "GITHUB_APP_ID", Class: "plain", Group: "github", Cells: []variables.MatrixCell{required("", true), optional("/web")}},
			{Key: "LOG_LEVEL", Class: "plain", Description: "How much the app logs", Cells: []variables.MatrixCell{required("", false), optional("/web")}},
			{Key: "API_TOKEN", Class: "secret", Cells: []variables.MatrixCell{required("", true), optional("/web")}},
			{Key: "GITHUB_APP_SLUG", Class: "plain", Group: "github", Scope: []string{"/web"}, Cells: []variables.MatrixCell{forbidden(""), required("/web", false)}},
			{Key: "GITHUB_KEY", Class: "secret", Group: "github", Cells: []variables.MatrixCell{required("", false), optional("/web")}},
			{Key: "SENTRY_DSN", Class: "sensitive", Group: "sentry", Cells: []variables.MatrixCell{required("", false), optional("/web")}},
		},
		Groups: []variables.MatrixGroup{{Key: "github", Description: "GitHub App"}, {Key: "sentry"}},
		Apps: []variables.AppResolution{
			{Name: "web", Folder: "/web", Missing: []variables.Cell{{Key: "LOG_LEVEL"}, {Key: "GITHUB_APP_SLUG", Folder: "/web"}, {Key: "GITHUB_KEY"}}},
			{Name: "api", Folder: "/api", Missing: []variables.Cell{{Key: "LOG_LEVEL"}, {Key: "GITHUB_KEY"}}},
		},
	}
}

func names(cells []guidedCell) []string {
	out := make([]string, 0, len(cells))
	for _, cell := range cells {
		out = append(out, cellName(cell.row.Key, cell.cell.Folder))
	}
	return out
}

func TestTheValuesThatNeedOneAreEachCellAnAppLacksWithAGroupsMembersTogether(t *testing.T) {
	got := names(guide{matrix: githubMatrix()}.needed())
	if want := []string{"LOG_LEVEL", "GITHUB_APP_SLUG in /web", "GITHUB_KEY"}; !slices.Equal(got, want) {
		t.Errorf("needed = %q, want %q: once each, ungrouped first, a scoped variable in its folder", got, want)
	}
}

func TestAGroupOrAVariableIsAskedForWhereverItIsRead(t *testing.T) {
	g := guide{matrix: githubMatrix()}
	if got, want := names(g.inGroup("github")), []string{"GITHUB_APP_ID", "GITHUB_APP_SLUG in /web", "GITHUB_KEY"}; !slices.Equal(got, want) {
		t.Errorf("inGroup(github) = %q, want %q", got, want)
	}
	if got, want := names(g.ofVariable("GITHUB_APP_SLUG")), []string{"GITHUB_APP_SLUG in /web"}; !slices.Equal(got, want) {
		t.Errorf("ofVariable(GITHUB_APP_SLUG) = %q, want %q", got, want)
	}
}

func TestEachGroupIsOfferedWithWhetherItIsActiveAndHowMuchOfItIsSet(t *testing.T) {
	matrix := githubMatrix()
	matrix.Groups = append(matrix.Groups, variables.MatrixGroup{Key: "stripe", Required: true})
	matrix.Rows = append(matrix.Rows, variables.MatrixRow{Key: "STRIPE_KEY", Class: "secret", Group: "stripe", Cells: []variables.MatrixCell{required("", false)}})

	var labels []string
	for _, option := range (guide{matrix: matrix}).groupOptions() {
		labels = append(labels, option.Label)
	}
	want := []string{"github (GitHub App) · active, 1 of 3 set", "sentry · off, none set", "stripe · required, 0 of 1 set"}
	if !slices.Equal(labels, want) {
		t.Errorf("group options = %q, want %q", labels, want)
	}
}

func TestAGuidedSetAsksForWhatIsNeededAndWritesOnlyWhatWasTypedOnceConfirmed(t *testing.T) {
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{
			"[1/3] LOG_LEVEL":               "debug",
			"[2/3] GITHUB_APP_SLUG in /web": "my-app",
			"[3/3] GITHUB_KEY":              "",
		}},
		choices: map[string]string{"What do you want to set?": "needed"},
		confirm: true,
	}
	var out bytes.Buffer

	pairs, err := guide{matrix: githubMatrix()}.walk(context.Background(), p, &out, "production")
	if err != nil {
		t.Fatalf("walk() = %v", err)
	}
	want := []envSetPair{{key: "LOG_LEVEL", value: "debug"}, {key: "GITHUB_APP_SLUG", value: "my-app", folder: "/web"}}
	if !slices.Equal(pairs, want) {
		t.Errorf("pairs = %#v, want %#v", pairs, want)
	}
	if got, want := p.answeringPrompt.asked, []string{
		"input [1/3] LOG_LEVEL plain · empty skips it · How much the app logs",
		"input [2/3] GITHUB_APP_SLUG in /web plain · empty skips it",
		"secret [3/3] GITHUB_KEY secret · empty skips it",
	}; !slices.Equal(got, want) {
		t.Errorf("asked %q, want %q", got, want)
	}
	if offered := p.offered["What do you want to set?"]; !slices.Equal(offered, []string{"The 3 that need a value", "A group…", "One variable…"}) {
		t.Errorf("offered %q", offered)
	}
	printed := out.String()
	for _, line := range []string{
		"production · 6 declared · 3 need a value",
		"group github (GitHub App) is active because GITHUB_APP_ID is set, so it needs:",
		"LOG_LEVEL                plain  debug",
		"GITHUB_APP_SLUG in /web  plain  my-app",
		"Left unset, and still needed by a deploy: GITHUB_KEY",
	} {
		if !strings.Contains(printed, line) {
			t.Errorf("printed:\n%s\nwant a line %q", printed, line)
		}
	}
	if !slices.Equal(p.asked, []string{"Write 2 values to production?"}) {
		t.Errorf("confirmed %q", p.asked)
	}
}

func TestAGuidedSetShowsASecretsSizeNeverItsValue(t *testing.T) {
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{"[1/1] SENTRY_DSN": "https://key@sentry.example/1"}},
		choices:         map[string]string{"What do you want to set?": "group", "Which group?": "sentry"},
		confirm:         true,
	}
	var out bytes.Buffer

	pairs, err := guide{matrix: githubMatrix()}.walk(context.Background(), p, &out, "production")
	if err != nil || len(pairs) != 1 {
		t.Fatalf("walk() = %v, %v, want SENTRY_DSN", pairs, err)
	}
	if strings.Contains(out.String(), "sentry.example") {
		t.Errorf("printed:\n%s\nwant a sensitive value never shown", out.String())
	}
	if !strings.Contains(out.String(), "group sentry needs:") || !strings.Contains(out.String(), "28 B") {
		t.Errorf("printed:\n%s\nwant the group's need and the value's size", out.String())
	}
	if !slices.Equal(p.answeringPrompt.asked, []string{"secret [1/1] SENTRY_DSN sensitive · empty skips it"}) {
		t.Errorf("asked %q, want a sensitive value typed hidden", p.answeringPrompt.asked)
	}
}

func TestAGuidedSetWritesNothingUnlessTheReviewIsConfirmed(t *testing.T) {
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{"[1/1] LOG_LEVEL": "debug"}},
		choices:         map[string]string{"What do you want to set?": "variable", "Which variable?": "LOG_LEVEL"},
	}
	var out bytes.Buffer

	pairs, err := guide{matrix: githubMatrix()}.walk(context.Background(), p, &out, "production")
	if err != nil || len(pairs) != 0 {
		t.Fatalf("walk() = %v, %v, want nothing to write", pairs, err)
	}
	if !strings.Contains(out.String(), "Nothing was written.") {
		t.Errorf("printed:\n%s\nwant it to say nothing was written", out.String())
	}
}

func TestAGuidedSetAbandonedPartWayWritesNothing(t *testing.T) {
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{"[1/3] LOG_LEVEL": "debug"}},
		choices:         map[string]string{"What do you want to set?": "needed"},
		confirm:         true,
	}

	pairs, err := guide{matrix: githubMatrix()}.walk(context.Background(), p, &bytes.Buffer{}, "production")
	if err == nil || !strings.Contains(err.Error(), "nothing was written") || len(pairs) != 0 {
		t.Fatalf("walk() = %v, %v, want an abandoned walk to write nothing", pairs, err)
	}
}

func TestAGuidedSetKeepsAValueAlreadySetWhenNothingIsTyped(t *testing.T) {
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{"[1/1] API_TOKEN": ""}},
		choices:         map[string]string{"What do you want to set?": "variable", "Which variable?": "API_TOKEN"},
		confirm:         true,
	}
	var out bytes.Buffer

	pairs, err := guide{matrix: githubMatrix()}.walk(context.Background(), p, &out, "production")
	if err != nil || len(pairs) != 0 {
		t.Fatalf("walk() = %v, %v, want nothing to write", pairs, err)
	}
	if !slices.Equal(p.answeringPrompt.asked, []string{"secret [1/1] API_TOKEN secret · set, empty keeps it"}) {
		t.Errorf("asked %q", p.answeringPrompt.asked)
	}
	if len(p.asked) != 0 {
		t.Errorf("confirmed %q, want no review of nothing", p.asked)
	}
}

func TestAGuidedSetNeverAsksForAValueTheEnvSourceOwns(t *testing.T) {
	owner := variables.EnvSource{ID: "infisical:p-1/prod", URLs: map[string]string{"": "https://app.infisical.com/p-1"}}
	p := &guidingPrompt{choices: map[string]string{"What do you want to set?": "needed"}}
	var out bytes.Buffer

	pairs, err := guide{matrix: githubMatrix(), owner: owner}.walk(context.Background(), p, &out, "production")
	if err != nil || len(pairs) != 0 {
		t.Fatalf("walk() = %v, %v, want nothing to write", pairs, err)
	}
	if len(p.answeringPrompt.asked) != 0 {
		t.Errorf("asked %q, want nothing", p.answeringPrompt.asked)
	}
	for _, want := range []string{"read from infisical:p-1/prod, so change them there", "LOG_LEVEL (https://app.infisical.com/p-1)", "GITHUB_APP_SLUG in /web"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("printed:\n%s\nwant %q", out.String(), want)
		}
	}
}

func TestAGuidedSetForOnePreviewEnvironmentAsksForItsOwnValues(t *testing.T) {
	matrix := githubMatrix()
	matrix.Rows[2].Cells[0].Overrides = []variables.Override{{Environment: "pr-12", Version: 1}}
	owner := variables.EnvSource{ID: "infisical:p-1/prod"}
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{"[1/1] API_TOKEN": ""}},
		choices:         map[string]string{"What do you want to set?": "variable", "Which variable?": "API_TOKEN"},
	}

	if _, err := (guide{matrix: matrix, owner: owner, environment: "pr-12"}).walk(context.Background(), p, &bytes.Buffer{}, "preview pr-12"); err != nil {
		t.Fatalf("walk() = %v", err)
	}
	if !slices.Equal(p.answeringPrompt.asked, []string{"secret [1/1] API_TOKEN secret · set, empty keeps it"}) {
		t.Errorf("asked %q, want pr-12's own value asked for, though the env source owns the tier's", p.answeringPrompt.asked)
	}
}

func TestAGuidedSetOfAProjectDeclaringNothingSaysSo(t *testing.T) {
	_, err := guide{}.walk(context.Background(), &guidingPrompt{}, &bytes.Buffer{}, "production")
	if err == nil || !strings.Contains(err.Error(), "declares no variable") {
		t.Fatalf("walk() = %v, want a refusal saying nothing is declared", err)
	}
}

func guidedDependencies(p *guidingPrompt) Dependencies {
	dependencies := askingDependencies(&p.answeringPrompt)
	dependencies.Guide = p
	return dependencies
}

func TestAGuidedSetStoresWhatWasTypedInTheTierItNames(t *testing.T) {
	project := setUpEnvFixture(t)
	p := &guidingPrompt{
		answeringPrompt: answeringPrompt{answers: map[string]string{
			"[1/4] STRIPE_API_KEY": "sk_live_typed",
			"[2/4] API_TOKEN":      "",
			"[3/4] LOG_LEVEL":      "debug",
			"[4/4] POSTHOG_ID":     "",
		}},
		choices: map[string]string{"What do you want to set?": "needed"},
		confirm: true,
	}

	var stdout, stderr bytes.Buffer
	if err := runGuidedSet(context.Background(), guidedDependencies(p), project.Root, envOptions{}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("runGuidedSet() = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	for key, want := range map[string]string{"STRIPE_API_KEY": "sk_live_typed", "LOG_LEVEL": "debug"} {
		if got := storedValue(t, project, cellAt(key)); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), "sk_live_typed") {
		t.Errorf("stdout=%s stderr=%s: a secret typed in is printed back", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "Left unset, and still needed by a deploy: API_TOKEN and POSTHOG_ID") {
		t.Errorf("stderr = %s, want what is still unset named before the confirmation", stderr.String())
	}
}

func TestAGuidedSetTakesNoFolder(t *testing.T) {
	project := setUpEnvFixture(t)
	p := &guidingPrompt{}

	err := runGuidedSet(context.Background(), guidedDependencies(p), project.Root, envOptions{folder: "/web"}, nil, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--folder /web") {
		t.Fatalf("runGuidedSet(--folder) = %v, want --folder sent to KEY=VALUE", err)
	}
}

func TestEnvSetWithNothingAfterItAndNobodyToAskIsRefused(t *testing.T) {
	cmd := NewCommand(newTestDependencies())
	cmd.SetArgs([]string{"set"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("Execute(set) = %v, want a refusal saying a terminal asks", err)
	}
}

func TestAValueEveryAppReadsFromItsOwnFolderIsNotNeededAtTheRoot(t *testing.T) {
	matrix := githubMatrix()
	matrix.Apps = []variables.AppResolution{{Name: "web", Folder: "/web", Missing: []variables.Cell{{Key: "GITHUB_KEY"}}}}
	if got, want := names(guide{matrix: matrix}.needed()), []string{"GITHUB_KEY"}; !slices.Equal(got, want) {
		t.Errorf("needed = %q, want %q: LOG_LEVEL and GITHUB_APP_SLUG reach every app already", got, want)
	}
}
