package providerprocess

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/confirmation"
	"github.com/ocelhq/ocel/cli/internal/run"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Questions struct {
	Respondent confirmation.Respondent
	run        *run.Run
}

func (q Questions) attended() bool {
	return q.Respondent != nil && q.Respondent.CanAnswer()
}

func (q Questions) answer(ctx context.Context, t transport, refused error) (bool, error) {
	question, asked := questionIn(refused)
	if !asked {
		return false, refused
	}
	if !q.attended() {
		return false, clierror.NewInputRequired(refused, printable(question.GetRemedy()))
	}
	client, err := t.Client()
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
	var holder confirmation.Holder
	if q.run != nil {
		holder = q.run
	}
	return q.Respondent.Confirm(ctx, confirmation.HolderOr(holder), confirmation.Question{Finding: printable(question.GetFinding()), Text: printable(question.GetPrompt())})
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
