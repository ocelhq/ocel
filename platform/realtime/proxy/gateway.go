package proxy

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	connect "connectrpc.com/connect"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	serverTokenTTL     = 60 * time.Second
	serverTokenSubject = "server"

	maxGatewayRetries      = 5
	firstGatewayRetryDelay = 100 * time.Millisecond
	maxGatewayRetryDelay   = 2 * time.Second

	dialOperation = "dial"
)

func NewGatewayTransport(client *http.Client, publishURL string) Transport {
	address, malformed := url.Parse(publishURL)
	return func(ctx context.Context, properties *bindingsv1.RealtimeProperties, req *realtimev1.PublishRequest) error {
		if err := RefuseOtherTransport(properties, bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY); err != nil {
			return err
		}
		if malformed != nil || address.Host == "" {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this runtime's gateway publish address %q is no URL with a host", publishURL))
		}
		credential, err := mintServerToken(properties.GetSigningKey(), properties.GetHost(), req)
		if err != nil {
			return connect.NewError(connect.CodeFailedPrecondition, err)
		}
		ctx, cancel := context.WithTimeout(ctx, PublishTimeout)
		defer cancel()
		for attempt := 0; ; attempt++ {
			isWritten, err := sendToGateway(ctx, client, address.String(), credential, req.GetEvent())
			if err == nil {
				return nil
			}
			if !isRetriable(isWritten, err) || attempt == maxGatewayRetries || !waitToRetry(ctx, attempt, retryAfterOf(err)) {
				return NewPublishError(ctx, req.GetChannel(), err)
			}
		}
	}
}

func sendToGateway(ctx context.Context, client *http.Client, address, credential, event string) (bool, error) {
	var isWritten atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaderField: func(string, []string) { isWritten.Store(true) }})
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(event))
	if err != nil {
		return false, err
	}
	post.Header.Set("Authorization", "Bearer "+credential)
	post.Header.Set("Content-Type", "application/json")
	_, err = Send(client, post, "the gateway")
	return isWritten.Load(), err
}

func isRetriable(isWritten bool, err error) bool {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.Status == http.StatusTooManyRequests
	}
	var dialing *net.OpError
	if isWritten || !errors.As(err, &dialing) || dialing.Op != dialOperation {
		return false
	}
	var lookup *net.DNSError
	return !errors.As(err, &lookup) || !lookup.IsNotFound
}

func retryAfterOf(err error) time.Duration {
	var refusal *Refusal
	if errors.As(err, &refusal) {
		return refusal.RetryAfter
	}
	return 0
}

func waitToRetry(ctx context.Context, attempt int, retryAfter time.Duration) bool {
	delay := retryAfter
	if delay == 0 {
		delay = rand.N(min(firstGatewayRetryDelay<<attempt, maxGatewayRetryDelay)) + 1
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

func mintServerToken(seed []byte, audience string, req *realtimev1.PublishRequest) (string, error) {
	if len(seed) != ed25519.SeedSize {
		return "", errors.New("the realtime binding's signing key is no Ed25519 seed")
	}
	id := make([]byte, 16)
	if _, err := cryptorand.Read(id); err != nil {
		return "", err
	}
	issuedAt := time.Now()
	return token.Sign(ed25519.NewKeyFromSeed(seed), token.Claims{
		Audience:  audience,
		IssuedAt:  issuedAt.Unix(),
		ExpiresAt: issuedAt.Add(serverTokenTTL).Unix(),
		Issuer:    "ocel:rt:" + req.GetRealtime(),
		ID:        base64.RawURLEncoding.EncodeToString(id),
		Subject:   serverTokenSubject,
		Ocel:      token.Grant{Channel: req.GetChannel(), Namespace: req.GetRealtime(), Operation: token.Publish},
	})
}
