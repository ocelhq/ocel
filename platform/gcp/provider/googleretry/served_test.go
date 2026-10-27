package googleretry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

func TestACallAnsweredNotFoundIsMadeAgainUntilGoogleServesWhatWasJustMade(t *testing.T) {
	t.Parallel()
	for name, absent := range map[string]error{
		"http 404":       &googleapi.Error{Code: http.StatusNotFound},
		"grpc not found": status.Error(codes.NotFound, "The database does not exist"),
		"wrapped":        fmt.Errorf("index the runs: %w", status.Error(codes.NotFound, "gone")),
	} {
		calls := 0
		err := googleretry.UntilServed(context.Background(), func() error {
			calls++
			if calls < 3 {
				return absent
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Errorf("UntilServed() over %s = %v after %d calls, want it to succeed on the third", name, err, calls)
		}
	}
}

func TestACallRefusedForAnythingButNotFoundIsNotWaitedOn(t *testing.T) {
	t.Parallel()
	calls := 0
	denied := &googleapi.Error{Code: http.StatusForbidden}
	err := googleretry.UntilServed(context.Background(), func() error {
		calls++
		return denied
	})
	if !errors.Is(err, denied) || calls != 1 {
		t.Errorf("UntilServed() over a refusal = %v after %d calls, want the refusal after one", err, calls)
	}
}

func TestACallAnsweredNotFoundIsMadeAgainWithinItsPatience(t *testing.T) {
	t.Parallel()
	calls := 0
	err := googleretry.UntilServedWithin(context.Background(), time.Minute, func() error {
		calls++
		if calls < 3 {
			return status.Error(codes.NotFound, "The database does not exist")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("UntilServedWithin() = %v after %d calls, want it to succeed on the third", err, calls)
	}
}

func TestACallStillAnsweredNotFoundOnceItsPatienceIsSpentReturnsThatAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	absent := status.Error(codes.NotFound, "The database does not exist")
	err := googleretry.UntilServedWithin(context.Background(), 0, func() error {
		calls++
		return absent
	})
	if !errors.Is(err, absent) || calls != 1 {
		t.Errorf("UntilServedWithin() with no patience = %v after %d calls, want the NotFound answer after one", err, calls)
	}
}
