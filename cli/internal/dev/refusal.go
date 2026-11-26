package dev

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variables/wording"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type localCommand struct {
	name    string
	source  valueSource
	watched bool
}

func (c localCommand) line() string {
	return "ocel " + c.name
}

func (c localCommand) afterChange() localCommand {
	c.watched = true
	return c
}

func (c localCommand) outcome() string {
	if c.watched {
		return "the app has been stopped"
	}
	return "the app has not been started"
}

func (c localCommand) remedy(them string) string {
	switch {
	case !c.watched && c.source.ownStore:
		return fmt.Sprintf("Set %s in %s, then run `%s` again.", them, project.DotenvFileName, c.line())
	case !c.watched:
		return fmt.Sprintf("Set %s in %s or %s, then run `%s` again.", them, c.source.id, project.LocalDotenvFileName, c.line())
	case c.source.ownStore:
		return fmt.Sprintf("Set %s in %s; `%s` restarts the app once %s.", them, project.DotenvFileName, c.line(), isSet(them))
	default:
		return fmt.Sprintf("Set %s in %s, which `%s` watches, or in %s and restart `%s`.", them, project.LocalDotenvFileName, c.line(), c.source.id, c.line())
	}
}

func isSet(them string) string {
	if them == "it" {
		return "it is"
	}
	return "they are"
}

func (c localCommand) hint(keys []string) string {
	if c.watched {
		return fmt.Sprintf("set %s in %s; `%s` restarts the app once %s", english.And(keys), c.source.where(), c.line(), isSet(pronoun(keys)))
	}
	return fmt.Sprintf("set %s in %s, then run `%s` again", english.And(keys), c.source.where(), c.line())
}

func describeRefusal(err error, dotfileKeys map[string]struct{}, run localCommand) error {
	var refusal *variables.MissingError
	if !errors.As(err, &refusal) {
		return err
	}

	keys := missingKeys(refusal.Problems)
	cells := refusal.Variables().GetCells()
	var b strings.Builder
	fmt.Fprintf(&b, "%s not ready — %s.\n\n", wording.VariablesAre(len(refusal.Problems)), run.outcome())
	written := map[string]bool{}
	for _, cell := range cells {
		group := cell.GetGroup()
		if group == "" {
			writeCell(&b, "  ", cell, cell.GetReason(), dotfileKeys, run)
			continue
		}
		if written[group] {
			continue
		}
		written[group] = true
		b.WriteString("  " + groupNeed(refusal, group) + "\n")
		for _, member := range cells {
			if member.GetGroup() != group {
				continue
			}
			reason := member.GetReason()
			if reason == variables.NoValue {
				reason = ""
			}
			writeCell(&b, "    ", member, reason, dotfileKeys, run)
		}
	}
	b.WriteString("\n" + run.remedy(pronoun(keys)))
	described := errors.New(b.String())
	var raised *clierror.Error
	if !errors.As(err, &raised) {
		return notReady{described}
	}
	coded := *raised
	coded.Hint = run.hint(keys)
	coded.Cause = described
	return notReady{&coded}
}

type notReady struct{ error }

func (n notReady) Unwrap() error { return n.error }

func refusesStart(err error) bool {
	return errors.As(err, new(notReady))
}

func writeCell(b *strings.Builder, lead string, cell *streamv1.MissingVariable, reason string, dotfileKeys map[string]struct{}, run localCommand) {
	b.WriteString(lead + cell.GetKey())
	if folder := cell.GetFolder(); folder != "" {
		b.WriteString(" (" + folder + ")")
	}
	if reason != "" {
		b.WriteString(": " + reason)
	}
	b.WriteString("\n")
	if hint := shellHint(cell.GetKey(), dotfileKeys, run); hint != "" {
		b.WriteString(lead + "  " + hint + "\n")
	}
}

func groupNeed(refusal *variables.MissingError, key string) string {
	name := "group " + key
	for _, group := range refusal.Groups {
		if group.GetKey() != key {
			continue
		}
		if description := group.GetDescription(); description != "" {
			name += " (" + description + ")"
		}
		if group.GetRequired() {
			return name + " is required, so it needs:"
		}
	}
	switch set := refusal.SetMembers[key]; len(set) {
	case 0:
		return name + " needs:"
	case 1:
		return name + " is active because " + set[0] + " is set, so it needs:"
	default:
		return name + " is active because " + english.And(set) + " are set, so it needs:"
	}
}

func pronoun(keys []string) string {
	if len(keys) == 1 {
		return "it"
	}
	return "them"
}

func missingKeys(problems []*resourcesv1.VariableProblem) []string {
	var keys []string
	for _, problem := range problems {
		if !slices.Contains(keys, problem.GetKey()) {
			keys = append(keys, problem.GetKey())
		}
	}
	return keys
}

func shellHint(key string, dotfileKeys map[string]struct{}, run localCommand) string {
	if _, inFile := dotfileKeys[key]; inFile {
		return ""
	}
	if _, inShell := os.LookupEnv(key); !inShell {
		return ""
	}
	return fmt.Sprintf("set in this shell, but `%s` resolves values from %s so every developer's run is the same", run.line(), run.source.where())
}

func refuseUnstatableBinding(source valueSource, apps []project.App, stated, configName string, scoped map[string][]string) error {
	var keys []string
	losing := make([]bool, len(apps))
	for key, folders := range scoped {
		if slices.Contains(folders, stated) {
			continue
		}
		lost := false
		for i, app := range apps {
			if slices.Contains(folders, app.Folder) {
				losing[i] = true
				lost = true
			}
		}
		if lost {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)

	var bindings []string
	for i, app := range apps {
		if losing[i] {
			bindings = append(bindings, app.Name+" binds "+folderLabel(app.Folder))
		}
	}

	return notReady{fmt.Errorf(`%s scoped to a folder this run cannot state — the app has not been started.

  %s

`+"`ocel dev` and `ocel run` spawn one child for the whole project and nothing tells it which app that child is, so the binding they state is %s. A scoped read refuses under it, even with the value in %s.\n\nfix: bind every app to the same folder in %s, or drop `folders:` from those declarations",
		scopedPlural(keys), strings.Join(bindings, "\n  "), folderLabel(stated), source.where(), configName)}
}

func scopedPlural(keys []string) string {
	if len(keys) == 1 {
		return keys[0] + " is"
	}
	return strings.Join(keys, ", ") + " are"
}

func folderLabel(folder string) string {
	if folder == "" {
		return "the project root"
	}
	return folder
}
