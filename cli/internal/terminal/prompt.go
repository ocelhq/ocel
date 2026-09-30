package terminal

import (
	"context"
	"errors"
	"io"
	"strings"

	"charm.land/huh/v2"
)

var promptTheme = huh.ThemeFunc(huh.ThemeDracula)

type Prompt struct {
	out      io.Writer
	in       io.Reader
	attended bool
}

func NewPrompt(out io.Writer, in io.Reader) Prompt {
	return Prompt{out: out, in: in, attended: IsTerminal(in) && IsTerminal(out)}
}

func (p Prompt) Attended() bool { return p.attended }

type Option struct {
	Name     string
	Summary  string
	Label    string
	Selected bool
}

func (o Option) label() string {
	switch {
	case o.Label != "":
		return o.Label
	case o.Summary != "":
		return o.Name + " — " + o.Summary
	}
	return o.Name
}

func (p Prompt) Confirm(ctx context.Context, question string) (bool, error) {
	if !p.attended {
		return p.confirmLine(ctx, question)
	}
	var answer bool
	err := p.run(ctx, huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&answer))
	if aborted(err) {
		return false, nil
	}
	return answer, err
}

func (p Prompt) Phrase(ctx context.Context, label, phrase string) (bool, error) {
	if !p.attended {
		return p.phraseLine(ctx, label, phrase)
	}
	var typed string
	err := p.run(ctx, huh.NewInput().
		Title("Type the "+label+" ("+phrase+") to confirm").
		Value(&typed))
	if aborted(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return phrase != "" && typed == phrase, nil
}

func (p Prompt) MultiSelect(ctx context.Context, title string, options []Option) ([]string, bool, error) {
	if !p.attended {
		return p.selectLine(ctx, title, options)
	}
	chosen := selectedNames(options)
	fields := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		fields = append(fields, huh.NewOption(o.label(), o.Name).Selected(o.Selected))
	}
	err := p.run(ctx, huh.NewMultiSelect[string]().
		Title(title).
		Description("Space toggles, Enter takes this set").
		Options(fields...).
		// FIXME: huh v2.0.3 subtracts the title height from the multiselect viewport
		// and description instead of the frame, so an unset Height scrolls one option at a time.
		// Drop this once the viewport sizing is fixed upstream.
		Height(len(fields)+2).
		Value(&chosen))
	if aborted(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return chosen, true, nil
}

func (p Prompt) Select(ctx context.Context, title string, options []Option) (string, bool, error) {
	if !p.attended {
		return p.selectOneLine(ctx, title, options)
	}
	fields := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		fields = append(fields, huh.NewOption(o.label(), o.Name))
	}
	var chosen string
	err := p.run(ctx, huh.NewSelect[string]().Title(title).Options(fields...).Value(&chosen))
	if aborted(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return chosen, true, nil
}

func (p Prompt) Input(ctx context.Context, title, description string) (string, bool, error) {
	if !p.attended {
		return p.inputLine(ctx, title, description)
	}
	var typed string
	err := p.run(ctx, huh.NewInput().Title(title).Description(description).Value(&typed))
	if aborted(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(typed), true, nil
}

func (p Prompt) AwaitEnter(ctx context.Context, question string) (bool, error) {
	if !p.attended {
		return p.awaitEnterLine(ctx, question)
	}
	answer := true
	err := p.run(ctx, huh.NewConfirm().Title(question).Affirmative("Continue").Negative("Stop").Value(&answer))
	if aborted(err) {
		return false, nil
	}
	return answer, err
}

func (p Prompt) run(ctx context.Context, field huh.Field) error {
	return huh.NewForm(huh.NewGroup(field)).
		WithTheme(promptTheme).
		WithInput(p.in).
		WithOutput(p.out).
		WithShowHelp(false).
		RunWithContext(ctx)
}

func aborted(err error) bool {
	return errors.Is(err, huh.ErrUserAborted) || errors.Is(err, io.EOF)
}

func selectedNames(options []Option) []string {
	var names []string
	for _, o := range options {
		if o.Selected {
			names = append(names, o.Name)
		}
	}
	return names
}
