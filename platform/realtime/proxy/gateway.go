package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	publishTimeout     = 10 * time.Second
	serverTokenTTL     = 60 * time.Second
	maxAnswerBytes     = 64 << 10
	serverTokenSubject = "server"
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
		ctx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		post, err := http.NewRequestWithContext(ctx, http.MethodPost, address.String(), strings.NewReader(req.GetEvent()))
		if err != nil {
			return err
		}
		post.Header.Set("Authorization", "Bearer "+credential)
		post.Header.Set("Content-Type", "application/json")
		res, err := client.Do(post)
		if err != nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("publish on %s: the gateway did not answer: %w", req.GetChannel(), err))
		}
		defer res.Body.Close()
		answer, _ := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes))
		if res.StatusCode/100 != 2 {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("publish on %s: the gateway refused it with status %d: %s", req.GetChannel(), res.StatusCode, answer))
		}
		return nil
	}
}

func mintServerToken(seed []byte, audience string, req *realtimev1.PublishRequest) (string, error) {
	if len(seed) != ed25519.SeedSize {
		return "", errors.New("the realtime binding's signing key is no Ed25519 seed")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
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
