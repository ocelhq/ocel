package provider

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type Question struct {
	Finding string
	Prompt  string
	Remedy  string
	Confirm func(ctx context.Context) error
}

type QuestionRefusal struct {
	refusal.Refusal
	Question Question
}

func (r QuestionRefusal) Unwrap() error { return r.Refusal }

func Ask(message string, question Question) error {
	return QuestionRefusal{
		Refusal:  refusal.Refusal{Code: refusal.CodeDenied, Message: message},
		Question: question,
	}
}

func QuestionOf(err error) (Question, bool) {
	var asked QuestionRefusal
	if errors.As(err, &asked) {
		return asked.Question, true
	}
	return Question{}, false
}
