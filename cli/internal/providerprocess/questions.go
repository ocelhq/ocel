package providerprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/run"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Prompt interface {
	Attended() bool
	Confirm(ctx context.Context, question string) (bool, error)
}

type Questions struct {
	Prompt Prompt
	Out    io.Writer
	IsJSON func() bool
	run    *run.Run
}

func (q Questions) attended() bool {
	return q.Prompt != nil && q.Out != nil && q.Prompt.Attended() && (q.IsJSON == nil || !q.IsJSON())
}

func (q Questions) holding(ask func() error) error {
	if q.run == nil {
		return ask()
	}
	return q.run.Ask(ask)
}

func (q Questions) answer(ctx context.Context, process *Process, refused error) (bool, error) {
	question, asked := questionIn(refused)
	if !asked {
		return false, refused
	}
	if !q.attended() {
		return false, clierror.NewInputRequired(refused, printable(question.GetRemedy()))
	}
	client, err := process.Client()
	if err != nil {
		return false, errors.Join(refused, fmt.Errorf("provider: confirm: %w", err))
	}
	for {
		confirmed, askErr := q.ask(ctx, question)
		if askErr != nil {
			return false, errors.Join(refused, askErr)
		}
		if !confirmed {
			return false, clierror.NewConfirmationRequired(refused, "")
		}
		_, confirmErr := client.Confirm(ctx, &contractv1.ConfirmRequest{QuestionId: question.GetId()})
		if next, again := questionIn(confirmErr); again {
			question, refused = next, confirmErr
			continue
		}
		if confirmErr != nil {
			return false, errors.Join(refused, fmt.Errorf("provider: confirm: %w", confirmErr))
		}
		return true, nil
	}
}

func (q Questions) ask(ctx context.Context, question *contractv1.Question) (bool, error) {
	var confirmed bool
	err := q.holding(func() (err error) {
		fmt.Fprintln(q.Out, printable(question.GetFinding()))
		confirmed, err = q.Prompt.Confirm(ctx, printable(question.GetPrompt()))
		return err
	})
	return confirmed, err
}

func questionIn(err error) (*contractv1.Question, bool) {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return nil, false
	}
	for _, detail := range wire.Details() {
		value, err := detail.Value()
		if err != nil {
			continue
		}
		if question, ok := value.(*contractv1.Question); ok && question.GetId() != "" {
			return question, true
		}
	}
	return nil, false
}

func printable(text string) string {
	return strings.Map(func(r rune) rune {
		if (r < 0x20 && r != '\n') || r == 0x7f {
			return -1
		}
		return r
	}, text)
}
