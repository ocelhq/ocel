package realtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/proxy"
)

const (
	appSyncService      = "appsync"
	appSyncPublishPath  = "/event"
	appSyncAPIHostLabel = ".appsync-api."
	publishTimeout      = 10 * time.Second
	maxAnswerBytes      = 64 << 10
)

type Config struct {
	Client      *http.Client
	Credentials aws.CredentialsProvider
	Retryer     aws.Retryer
	Region      string
}

type unacceptedError struct {
	status int
	answer []byte
}

func (e *unacceptedError) Error() string {
	return fmt.Sprintf("AppSync refused it with status %d: %s", e.status, e.answer)
}

func (e *unacceptedError) isThrottle() bool {
	if e.status == http.StatusTooManyRequests {
		return true
	}
	var answer struct {
		Errors []struct {
			ErrorType string `json:"errorType"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(e.answer, &answer)
	for _, refusal := range answer.Errors {
		if _, throttle := retry.DefaultThrottleErrorCodes[refusal.ErrorType]; throttle {
			return true
		}
	}
	return false
}

func NewAppSyncTransport(cfg Config) proxy.Transport {
	signer := v4.NewSigner()
	return func(ctx context.Context, properties *bindingsv1.RealtimeProperties, req *realtimev1.PublishRequest) error {
		if err := proxy.RefuseOtherTransport(properties, bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_APPSYNC_EVENTS); err != nil {
			return err
		}
		failed := func(code connect.Code, err error) error {
			return connect.NewError(code, fmt.Errorf("publish on %s: %w", req.GetChannel(), err))
		}
		body, err := json.Marshal(struct {
			Channel string   `json:"channel"`
			Events  []string `json:"events"`
		}{Channel: req.GetChannel(), Events: []string{req.GetEvent()}})
		if err != nil {
			return failed(connect.CodeInternal, err)
		}
		ctx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		publish := func() ([]byte, error) {
			creds, err := cfg.Credentials.Retrieve(ctx)
			if err != nil {
				return nil, fmt.Errorf("read the runtime's AWS credentials: %w", err)
			}
			post, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+properties.GetHost()+appSyncPublishPath, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			post.Header.Set("Content-Type", "application/json")
			sum := sha256.Sum256(body)
			if err := signer.SignHTTP(ctx, creds, post, hex.EncodeToString(sum[:]), appSyncService, findRegion(properties.GetHost(), cfg.Region), time.Now()); err != nil {
				return nil, err
			}
			res, err := cfg.Client.Do(post)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			answer, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes))
			if res.StatusCode/100 != 2 {
				return nil, &unacceptedError{status: res.StatusCode, answer: answer}
			}
			return answer, nil
		}
		for attempt := 1; ; attempt++ {
			answer, err := publish()
			if err == nil {
				return refuseFailedEvents(answer, failed)
			}
			var unaccepted *unacceptedError
			if !errors.As(err, &unaccepted) || !unaccepted.isThrottle() || attempt >= cfg.Retryer.MaxAttempts() {
				return failed(connect.CodeUnavailable, err)
			}
			delay, delayErr := cfg.Retryer.RetryDelay(attempt, err)
			if delayErr != nil {
				return failed(connect.CodeUnavailable, err)
			}
			select {
			case <-ctx.Done():
				return failed(connect.CodeDeadlineExceeded, err)
			case <-time.After(delay):
			}
		}
	}
}

func refuseFailedEvents(answer []byte, failed func(connect.Code, error) error) error {
	var published struct {
		Failed []json.RawMessage `json:"failed"`
	}
	if json.Unmarshal(answer, &published) == nil && len(published.Failed) > 0 {
		return failed(connect.CodeInternal, fmt.Errorf("AppSync failed the event: %s", answer))
	}
	return nil
}

func findRegion(host, fallback string) string {
	if _, rest, found := strings.Cut(host, appSyncAPIHostLabel); found {
		if region, _, found := strings.Cut(rest, "."); found && region != "" {
			return region
		}
	}
	return fallback
}
