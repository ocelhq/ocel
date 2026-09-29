package provider_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func knownHostQuestion(confirmed *bool) provider.Question {
	return provider.Question{
		Finding: "the host key for 203.0.113.10 is in none of ~/.ssh/known_hosts",
		Prompt:  "Trust that key?",
		Confirm: func(context.Context) error { *confirmed = true; return nil },
	}
}

func TestAQuestionIsADeniedRefusalThatCarriesWhatAYesWouldDo(t *testing.T) {
	t.Parallel()

	confirmed := false
	err := fmt.Errorf("open the session: %w", provider.Ask("run ssh-keyscan and try again", knownHostQuestion(&confirmed)))

	if code, ok := provider.RefusedCode(err); !ok || code != refusal.CodeDenied {
		t.Errorf("RefusedCode() = %q, %t, want a %s refusal", code, ok, refusal.CodeDenied)
	}
	asked, ok := provider.QuestionOf(err)
	if !ok {
		t.Fatal("QuestionOf() found no question in the refusal Ask returned")
	}
	if err := asked.Confirm(context.Background()); err != nil || !confirmed {
		t.Errorf("Confirm() = %v, confirmed %t, want the provider's own action run", err, confirmed)
	}
}

func TestAQuestionReachesTheWireAsAPermissionDeniedThatStillHoldsIt(t *testing.T) {
	t.Parallel()

	confirmed := false
	wire := provider.RefusalError(provider.Ask("run ssh-keyscan and try again", knownHostQuestion(&confirmed)))

	var coded *connect.Error
	if !errors.As(wire, &coded) || coded.Code() != connect.CodePermissionDenied || coded.Message() != "run ssh-keyscan and try again" {
		t.Fatalf("RefusalError() = %v, want PermissionDenied with the refusal's own message", wire)
	}
	if code, ok := provider.RefusedCode(wire); !ok || code != refusal.CodeDenied {
		t.Errorf("RefusedCode() over the wire = %q, %t, want %s", code, ok, refusal.CodeDenied)
	}
	if _, ok := provider.QuestionOf(wire); !ok {
		t.Error("QuestionOf() lost the question once the refusal became a wire error")
	}
}

func TestAnOrdinaryRefusalAsksNothing(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		refusal.Refuse(refusal.CodeDenied, "no"),
		provider.RefusalError(refusal.Refuse(refusal.CodeDenied, "no")),
		fmt.Errorf("wrapped: %w", errors.New("no")),
		nil,
	} {
		if _, ok := provider.QuestionOf(err); ok {
			t.Errorf("QuestionOf(%v) found a question in an error that asks none", err)
		}
	}
}
