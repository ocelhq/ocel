package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	connect "connectrpc.com/connect"
)

const (
	PublishTimeout = 10 * time.Second
	maxAnswerBytes = 64 << 10
)

type Refusal struct {
	Transport string
	Status    int
	Answer    []byte
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s refused it with status %d: %s", r.Transport, r.Status, r.Answer)
}

func Send(client *http.Client, post *http.Request, transport string) ([]byte, error) {
	res, err := client.Do(post)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes))
	if res.StatusCode/100 != 2 {
		return nil, &Refusal{Transport: transport, Status: res.StatusCode, Answer: answer}
	}
	return answer, nil
}

func NewPublishError(ctx context.Context, channel string, err error) error {
	return connect.NewError(publishErrorCode(ctx, err), fmt.Errorf("publish on %s: %w", channel, err))
}

func publishErrorCode(ctx context.Context, err error) connect.Code {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return connect.CodeCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return connect.CodeDeadlineExceeded
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		return connect.CodeUnavailable
	}
	switch {
	case refusal.Status == http.StatusBadRequest:
		return connect.CodeInvalidArgument
	case refusal.Status == http.StatusUnauthorized, refusal.Status == http.StatusForbidden:
		return connect.CodePermissionDenied
	case refusal.Status == http.StatusTooManyRequests:
		return connect.CodeResourceExhausted
	case refusal.Status/100 == 4:
		return connect.CodeFailedPrecondition
	default:
		return connect.CodeUnavailable
	}
}
