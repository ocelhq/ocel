package confirmation

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/jobinput"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

var ErrNobodyToAsk = errors.New("nobody is here to answer a question")

type Question struct {
	Finding      string
	FindingShown bool
	Text         string
	Label        string
	Phrase       string
}

type Holder interface {
	Hold(waiting *streamv1.WaitingEvent) (resume func(reason string))
}

type Respondent interface {
	CanAnswer() bool
	Confirm(ctx context.Context, holder Holder, question Question) (bool, error)
}

type Prompt interface {
	Attended() bool
	Confirm(ctx context.Context, question string) (bool, error)
	Phrase(ctx context.Context, label, phrase string) (bool, error)
}

type Terminal struct {
	Prompt Prompt
	Out    io.Writer
	IsJSON func() bool
}

func (t Terminal) CanAnswer() bool {
	return t.Prompt != nil && t.Prompt.Attended() && (t.IsJSON == nil || !t.IsJSON())
}

func (t Terminal) Confirm(ctx context.Context, holder Holder, question Question) (bool, error) {
	resume := holder.Hold(&streamv1.WaitingEvent{})
	defer resume("answered")
	if question.Finding != "" && !question.FindingShown && t.Out != nil {
		fmt.Fprintln(t.Out, question.Finding)
	}
	if question.Phrase != "" {
		return t.Prompt.Phrase(ctx, question.Label, question.Phrase)
	}
	return t.Prompt.Confirm(ctx, question.Text)
}

type attended struct{ Prompt }

func (attended) Attended() bool { return true }

func AlwaysAttended(prompt Prompt) Prompt {
	return attended{prompt}
}

func OnTerminal(out io.Writer, in io.Reader) Terminal {
	return Terminal{Prompt: AlwaysAttended(prompt.New(out, in)), Out: out}
}

type Job struct {
	Answers *jobinput.Answers
}

func (Job) CanAnswer() bool { return true }

func (j Job) Confirm(ctx context.Context, holder Holder, question Question) (bool, error) {
	return j.Answers.Confirm(ctx, holder, question.Finding, question.Text)
}

type noHold struct{}

func (noHold) Hold(*streamv1.WaitingEvent) func(string) { return func(string) {} }

func HolderOr(holder Holder) Holder {
	if holder == nil {
		return noHold{}
	}
	return holder
}

type Nobody struct{}

func (Nobody) CanAnswer() bool { return false }

func (Nobody) Confirm(context.Context, Holder, Question) (bool, error) {
	return false, ErrNobodyToAsk
}
