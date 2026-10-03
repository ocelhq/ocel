package realtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
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
)

type Config struct {
	Client      *http.Client
	Credentials aws.CredentialsProvider
	Retryer     aws.RetryerV2
	Region      string
}

type unsentError struct{ err error }

func (e *unsentError) Error() string { return "AppSync was never sent it: " + e.err.Error() }

func (e *unsentError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var unsent *unsentError
	return errors.As(err, &unsent) || isThrottle(err)
}

func isThrottle(err error) bool {
	var refusal *proxy.Refusal
	if !errors.As(err, &refusal) {
		return false
	}
	if refusal.Status == http.StatusTooManyRequests {
		return true
	}
	var answer struct {
		Errors []struct {
			ErrorType string `json:"errorType"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(refusal.Answer, &answer)
	for _, refused := range answer.Errors {
		if _, throttle := retry.DefaultThrottleErrorCodes[refused.ErrorType]; throttle {
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
		ctx, cancel := context.WithTimeout(ctx, proxy.PublishTimeout)
		defer cancel()
		publish := func() ([]byte, error) {
			creds, err := cfg.Credentials.Retrieve(ctx)
			if err != nil {
				return nil, fmt.Errorf("read the runtime's AWS credentials: %w", err)
			}
			var written atomic.Bool
			traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaders: func() { written.Store(true) }})
			post, err := http.NewRequestWithContext(traced, http.MethodPost, "https://"+properties.GetHost()+appSyncPublishPath, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			post.Header.Set("Content-Type", "application/json")
			sum := sha256.Sum256(body)
			if err := signer.SignHTTP(ctx, creds, post, hex.EncodeToString(sum[:]), appSyncService, findRegion(properties.GetHost(), cfg.Region), time.Now()); err != nil {
				return nil, err
			}
			answer, err := proxy.Send(cfg.Client, post, "AppSync")
			var refusal *proxy.Refusal
			if err != nil && !written.Load() && !errors.As(err, &refusal) {
				return nil, &unsentError{err: err}
			}
			return answer, err
		}
		exhausted := func(err error) error {
			if isThrottle(err) {
				return failed(connect.CodeResourceExhausted, err)
			}
			return proxy.NewPublishError(ctx, req.GetChannel(), err)
		}
		releaseRetry := func(error) error { return nil }
		for attempt := 1; ; attempt++ {
			releaseAttempt, err := cfg.Retryer.GetAttemptToken(ctx)
			if err != nil {
				return proxy.NewPublishError(ctx, req.GetChannel(), err)
			}
			answer, err := publish()
			_ = releaseRetry(err)
			_ = releaseAttempt(err)
			if err == nil {
				return refuseFailedEvents(answer, failed)
			}
			if ctx.Err() != nil || !isRetryable(err) {
				return proxy.NewPublishError(ctx, req.GetChannel(), err)
			}
			if attempt >= cfg.Retryer.MaxAttempts() {
				return exhausted(err)
			}
			release, quotaErr := cfg.Retryer.GetRetryToken(ctx, err)
			if quotaErr != nil {
				return exhausted(errors.Join(err, quotaErr))
			}
			releaseRetry = release
			delay, delayErr := cfg.Retryer.RetryDelay(attempt, err)
			if delayErr != nil {
				return exhausted(errors.Join(err, delayErr))
			}
			select {
			case <-ctx.Done():
				return proxy.NewPublishError(ctx, req.GetChannel(), err)
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
