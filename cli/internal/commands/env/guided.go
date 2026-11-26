package env

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variables/wording"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type GuidePrompt interface {
	commands.ValuePrompt
	Select(ctx context.Context, title string, options []prompt.Option) (string, bool, error)
	Confirm(ctx context.Context, question string) (bool, error)
}

var errAbandoned = errors.New("the questions were left unanswered, so nothing was written")

func canGuide(dependencies Dependencies, stdin io.Reader) bool {
	return dependencies.Guide != nil && dependencies.CanAsk(stdin)
}

func runGuidedSet(ctx context.Context, dependencies Dependencies, cwd string, opts envOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	if opts.folder != "" {
		return fmt.Errorf("`ocel env set` with no arguments asks for each value in the folders that read it, so --folder takes a KEY=VALUE: `ocel env set KEY=VALUE --folder %s`", opts.folder)
	}
	return withEnvProviderOfferingVariablesKey(ctx, dependencies, cwd, opts, "ocel env set", []scopev1.Scope{scopev1.Scope_SCOPE_VARIABLES_WRITE}, stdin, stderr, func(ctx context.Context, run *run.Run, provider *providerprocess.Provider, cfg *project.Project, _ *contractv1.PreflightResponse) error {
		declarations := variables.NewDeclarations(variables.Store{Provider: provider, Project: cfg, Tier: opts.tier(), DefinitionsOnly: true}, variables.ScopeOf(cfg, opts.tier(), opts.environment))
		err := collecting(ctx, run, cfg, func(ctx context.Context, output io.Writer) error {
			_, err := declaration.Collect(ctx, cfg, declarations, io.Discard, output)
			return err
		})
		if err != nil {
			return err
		}
		values := envValues(provider, cfg, opts)
		owner, err := ownerOf(ctx, values, opts)
		if err != nil {
			return err
		}
		walked := guide{matrix: declarations.Matrix(nil), owner: owner, environment: opts.environment}
		var pairs []envSetPair
		if err := run.Ask(func() (err error) {
			pairs, err = walked.walk(ctx, dependencies.Guide, stderr, guidedTier(opts))
			return err
		}); err != nil || len(pairs) == 0 {
			return err
		}
		definitions, groups, err := withImpliedDeclarations(cfg, opts, declarations.Definitions(), declarations.Groups())
		if err != nil {
			return err
		}
		if err := refuseUnsettable(cfg, opts, definitions, owner, pairs); err != nil {
			return err
		}
		return writePairs(ctx, run, provider, cfg, values, owner, definitions, groups, pairs, opts, stdout)
	})
}

func guidedTier(opts envOptions) string {
	if opts.environment != "" {
		return "preview environment " + opts.environment
	}
	return tierName(opts)
}

type guide struct {
	matrix      variables.Matrix
	owner       variables.EnvSource
	environment string
}

type guidedCell struct {
	row  variables.MatrixRow
	cell variables.MatrixCell
	set  bool
}

func (c guidedCell) at() variables.Cell {
	return variables.Cell{Key: c.row.Key, Folder: c.cell.Folder}
}

func cellName(key, folder string) string {
	if folder == "" {
		return key
	}
	return key + " in " + folder
}

func (c guidedCell) name() string {
	return cellName(c.row.Key, c.cell.Folder)
}

func isSecretClass(class string) bool {
	return class == variables.ClassName(resourcesv1.VariableClass_VARIABLE_CLASS_SECRET) || class == variables.ClassName(resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE)
}

func (g guide) isSet(cell variables.MatrixCell) bool {
	if g.environment == "" {
		return cell.Set || cell.Reference != nil
	}
	return slices.ContainsFunc(cell.Overrides, func(override variables.Override) bool { return override.Environment == g.environment })
}

func (g guide) cellsOf(row variables.MatrixRow) []guidedCell {
	var out []guidedCell
	for _, cell := range row.Cells {
		if cell.State == variables.CellForbidden || (len(row.Scope) == 0 && cell.Folder != "") {
			continue
		}
		out = append(out, guidedCell{row: row, cell: cell, set: g.isSet(cell)})
	}
	return out
}

func (g guide) groupRank(group string) int {
	if group == "" {
		return -1
	}
	return slices.IndexFunc(g.matrix.Groups, func(candidate variables.MatrixGroup) bool { return candidate.Key == group })
}

func (g guide) stillNeeded(cell guidedCell, typed map[variables.Cell]bool) bool {
	if cell.set || typed[cell.at()] || cell.cell.State != variables.CellRequired {
		return false
	}
	if cell.row.Group != "" && !g.active(cell.row.Group, typed) {
		return false
	}
	return len(g.matrix.Apps) == 0 || slices.ContainsFunc(g.matrix.Apps, func(app variables.AppResolution) bool { return slices.Contains(app.Missing, cell.at()) })
}

func (g guide) needed() []guidedCell {
	var out []guidedCell
	for _, row := range g.matrix.Rows {
		for _, cell := range g.cellsOf(row) {
			if g.stillNeeded(cell, nil) {
				out = append(out, cell)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b guidedCell) int { return cmp.Compare(g.groupRank(a.row.Group), g.groupRank(b.row.Group)) })
	return out
}

func (g guide) inGroup(group string) []guidedCell {
	var out []guidedCell
	for _, row := range g.matrix.Rows {
		if row.Group == group {
			out = append(out, g.cellsOf(row)...)
		}
	}
	return out
}

func (g guide) ofVariable(key string) []guidedCell {
	for _, row := range g.matrix.Rows {
		if row.Key == key {
			return g.cellsOf(row)
		}
	}
	return nil
}

func (g guide) group(key string) variables.MatrixGroup {
	for _, group := range g.matrix.Groups {
		if group.Key == key {
			return group
		}
	}
	return variables.MatrixGroup{Key: key}
}

func (g guide) setMembers(group string, typed map[variables.Cell]bool) []string {
	var out []string
	for _, row := range g.matrix.Rows {
		if row.Group != group {
			continue
		}
		if slices.ContainsFunc(g.cellsOf(row), func(cell guidedCell) bool { return cell.set || typed[cell.at()] }) {
			out = append(out, row.Key)
		}
	}
	return out
}

func (g guide) active(group string, typed map[variables.Cell]bool) bool {
	return g.group(group).Required || len(g.setMembers(group, typed)) > 0
}

func (g guide) groupOptions() []prompt.Option {
	options := make([]prompt.Option, 0, len(g.matrix.Groups))
	for _, group := range g.matrix.Groups {
		members := 0
		for _, row := range g.matrix.Rows {
			if row.Group == group.Key {
				members++
			}
		}
		if members == 0 {
			continue
		}
		label := group.Key
		if group.Description != "" {
			label += " (" + group.Description + ")"
		}
		set := len(g.setMembers(group.Key, nil))
		switch {
		case group.Required:
			label += fmt.Sprintf(" · required, %d of %d set", set, members)
		case set > 0:
			label += fmt.Sprintf(" · active, %d of %d set", set, members)
		default:
			label += " · off, none set"
		}
		options = append(options, prompt.Option{Name: group.Key, Label: terminal.EscapeControls(label)})
	}
	return options
}

func (g guide) variableOptions() []prompt.Option {
	options := make([]prompt.Option, 0, len(g.matrix.Rows))
	for _, row := range g.matrix.Rows {
		cells := g.cellsOf(row)
		if len(cells) == 0 {
			continue
		}
		state := "not set"
		if !slices.ContainsFunc(cells, func(cell guidedCell) bool { return !cell.set }) {
			state = "set"
		}
		options = append(options, prompt.Option{Name: row.Key, Label: terminal.EscapeControls(row.Key + " · " + row.Class + " · " + state)})
	}
	return options
}

func (g guide) coordinate(cell guidedCell) variables.Coordinate {
	return variables.Coordinate{Cell: cell.at(), Environment: g.environment}
}

func (g guide) split(cells []guidedCell) (open, owned, referenced []guidedCell) {
	for _, cell := range cells {
		switch {
		case g.owner.RefuseOwned(g.coordinate(cell)) != nil:
			owned = append(owned, cell)
		case g.environment == "" && cell.cell.Reference != nil:
			referenced = append(referenced, cell)
		default:
			open = append(open, cell)
		}
	}
	return open, owned, referenced
}

func (g guide) walk(ctx context.Context, asker GuidePrompt, out io.Writer, tier string) ([]envSetPair, error) {
	if len(g.matrix.Rows) == 0 {
		return nil, errors.New("this project declares no variable, so there is nothing to set: declare one in a defineEnv call")
	}
	needed := g.needed()
	fmt.Fprintf(out, "%s · %d declared · %d need a value\n", tier, len(g.matrix.Rows), len(needed))
	chosen, err := g.choose(ctx, asker, needed)
	if err != nil {
		return nil, err
	}
	open, owned, referenced := g.split(chosen)
	g.sayUnsettable(out, owned, referenced)
	if len(open) == 0 {
		return nil, nil
	}
	typed, err := g.ask(ctx, asker, out, open)
	if err != nil {
		return nil, err
	}
	if len(typed) == 0 {
		fmt.Fprintln(out, "Nothing was typed, so nothing was written.")
		return nil, nil
	}
	g.review(out, open, typed, tier)
	confirmed, err := asker.Confirm(ctx, fmt.Sprintf("Write %s to %s?", english.Count(len(typed), "value"), tier))
	if err != nil {
		return nil, err
	}
	if !confirmed {
		fmt.Fprintln(out, "Nothing was written.")
		return nil, nil
	}
	pairs := make([]envSetPair, 0, len(typed))
	for _, answer := range typed {
		pairs = append(pairs, envSetPair{key: answer.cell.row.Key, value: answer.value, folder: answer.cell.cell.Folder})
	}
	return pairs, nil
}

func (g guide) choose(ctx context.Context, asker GuidePrompt, needed []guidedCell) ([]guidedCell, error) {
	var options []prompt.Option
	if len(needed) > 0 {
		label := fmt.Sprintf("The %d that need a value", len(needed))
		if len(needed) == 1 {
			label = "The one that needs a value"
		}
		options = append(options, prompt.Option{Name: "needed", Label: label})
	}
	groups := g.groupOptions()
	if len(groups) > 0 {
		options = append(options, prompt.Option{Name: "group", Label: "A group…"})
	}
	options = append(options, prompt.Option{Name: "variable", Label: "One variable…"})
	choice, answered, err := asker.Select(ctx, "What do you want to set?", options)
	if err != nil || !answered {
		return nil, cmp.Or(err, errAbandoned)
	}
	switch choice {
	case "needed":
		return needed, nil
	case "group":
		group, answered, err := asker.Select(ctx, "Which group?", groups)
		if err != nil || !answered {
			return nil, cmp.Or(err, errAbandoned)
		}
		return g.inGroup(group), nil
	default:
		key, answered, err := asker.Select(ctx, "Which variable?", g.variableOptions())
		if err != nil || !answered {
			return nil, cmp.Or(err, errAbandoned)
		}
		return g.ofVariable(key), nil
	}
}

func (g guide) sayUnsettable(out io.Writer, owned, referenced []guidedCell) {
	if len(owned) > 0 {
		verb, pronoun := "are", "them"
		if len(owned) == 1 {
			verb, pronoun = "is", "it"
		}
		fmt.Fprintf(out, "%s %s read from %s, so change %s there:\n", english.Count(len(owned), "value"), verb, g.owner.ID, pronoun)
		for _, cell := range owned {
			line := "  " + cell.name()
			if url := g.owner.URLs[cell.cell.Folder]; url != "" {
				line += " (" + url + ")"
			}
			fmt.Fprintln(out, terminal.EscapeControls(line))
		}
	}
	for _, cell := range referenced {
		reference := cell.cell.Reference
		fmt.Fprintln(out, terminal.EscapeControls(fmt.Sprintf("%s reads %s/%s, so change it where that value is set", cell.name(), reference.Slug, cellName(reference.Key, reference.Folder))))
	}
}

type typedValue struct {
	cell  guidedCell
	value string
}

func (g guide) describe(cell guidedCell) string {
	parts := []string{cell.row.Class, "empty skips it"}
	if cell.set {
		parts[1] = "set, empty keeps it"
	}
	if g.owner.CanSet(g.coordinate(cell)) {
		parts = append(parts, "saving to "+g.owner.ID)
	}
	if cell.row.Description != "" {
		parts = append(parts, cell.row.Description)
	}
	return terminal.EscapeControls(strings.Join(parts, " · "))
}

func (g guide) ask(ctx context.Context, asker GuidePrompt, out io.Writer, cells []guidedCell) ([]typedValue, error) {
	var typed []typedValue
	group := ""
	for i, cell := range cells {
		if cell.row.Group != "" && cell.row.Group != group {
			described := g.group(cell.row.Group)
			fmt.Fprintln(out, terminal.EscapeControls(wording.GroupNeed(described.Key, described.Description, described.Required, g.setMembers(described.Key, nil))))
		}
		group = cell.row.Group
		ask := asker.Input
		if isSecretClass(cell.row.Class) {
			ask = asker.Secret
		}
		value, answered, err := ask(ctx, terminal.EscapeControls(fmt.Sprintf("[%d/%d] %s", i+1, len(cells), cell.name())), g.describe(cell))
		if err != nil {
			return nil, err
		}
		if !answered {
			return nil, errAbandoned
		}
		if value != "" {
			typed = append(typed, typedValue{cell: cell, value: value})
		}
	}
	return typed, nil
}

func (g guide) review(out io.Writer, asked []guidedCell, typed []typedValue, tier string) {
	fmt.Fprintf(out, "Ready to write to %s:\n", tier)
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	written := map[variables.Cell]bool{}
	for _, answer := range typed {
		written[answer.cell.at()] = true
		shown := terminal.EscapeControls(answer.value)
		if isSecretClass(answer.cell.row.Class) {
			shown = english.Bytes(uint64(len(answer.value)))
		}
		if g.owner.CanSet(g.coordinate(answer.cell)) {
			shown += " → " + g.owner.ID
		}
		fmt.Fprintf(table, "  %s\t%s\t%s\n", answer.cell.name(), answer.cell.row.Class, shown)
	}
	_ = table.Flush()
	var unset []string
	for _, cell := range asked {
		if g.stillNeeded(cell, written) {
			unset = append(unset, cell.name())
		}
	}
	if len(unset) > 0 {
		fmt.Fprintln(out, "Left unset, and still needed by a deploy: "+english.And(unset))
	}
}
