package portforward

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestAnAnswerIsTakenEvenWhenTheStreamHasAlsoEndedByTheTimeItIsRead(t *testing.T) {
	for range 100 {
		answered := make(chan *contractv1.ForwardPortsResponse, 1)
		ended := make(chan error, 1)
		answered <- &contractv1.ForwardPortsResponse{Unforwarded: []string{"files--files"}}
		ended <- nil

		resp, err := awaitAnswer(answered, ended)
		if err != nil || len(resp.GetUnforwarded()) != 1 {
			t.Fatalf("awaitAnswer() left %v unforwarded, %v, want the answer the provider sent before its stream ended", resp.GetUnforwarded(), err)
		}
		if got := <-ended; got != nil {
			t.Fatalf("the stream's end was replaced by %v, want it kept for Close", got)
		}
	}
}

func TestClosingTheForwardsReportsHowTheirStreamFailed(t *testing.T) {
	ended := make(chan error, 1)
	ended <- errors.New("provider: provider connection lost")
	forwards := &Forwards{stop: func() {}, ended: ended}

	if err := forwards.Close(); err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Errorf("Close() = %v, want the stream's failure", err)
	}
}

func TestClosingTheForwardsIsNoFailureWhenTheirStreamEndedBecauseItWasClosed(t *testing.T) {
	ended := make(chan error, 1)
	ended <- fmt.Errorf("provider: ForwardPorts was cancelled: %w", connect.NewError(connect.CodeCanceled, context.Canceled))
	forwards := &Forwards{stop: func() {}, ended: ended}

	if err := forwards.Close(); err != nil {
		t.Errorf("Close() = %v, want nil: closing the forwards is what ended their stream", err)
	}
}
