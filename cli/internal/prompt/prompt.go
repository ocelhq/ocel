package prompt

import (
	"context"
	"errors"
	"io"
	"os"

	"charm.land/huh/v2"
	"github.com/mattn/go-isatty"
)

type Prompter struct {
	out io.Writer
	in  io.Reader
	tty bool
}

func New(out io.Writer, in io.Reader) Prompter {
	return Prompter{out: out, in: in, tty: isTerminal(in) && isTerminal(out)}
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}

type Option struct {
	Name     string
	Summary  string
	Selected bool
}

func (p Prompter) Confirm(ctx context.Context, question string) (bool, error) {
	if !p.tty {
		return p.confirmLine(ctx, question)
	}
	var answer bool
	err := p.run(ctx, huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&answer))
	if aborted(err) {
		return false, nil
	}
	return answer, err
}

func (p Prompter) Phrase(ctx context.Context, label, phrase string) (bool, error) {
	if !p.tty {
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

func (p Prompter) Select(ctx context.Context, title string, options []Option) ([]string, bool, error) {
	if !p.tty {
		return p.selectLine(ctx, title, options)
	}
	chosen := selectedNames(options)
	fields := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		field := huh.NewOption(o.Name, o.Name).Selected(o.Selected)
		if o.Summary != "" {
			field = huh.NewOption(o.Name+" — "+o.Summary, o.Name).Selected(o.Selected)
		}
		fields = append(fields, field)
	}
	err := p.run(ctx, huh.NewMultiSelect[string]().
		Title(title).
		Description("Space toggles, Enter takes this set").
		Options(fields...).
		Value(&chosen))
	if aborted(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return chosen, true, nil
}

func (p Prompter) run(ctx context.Context, field huh.Field) error {
	return huh.NewForm(huh.NewGroup(field)).
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
