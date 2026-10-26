package proxy

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	realtimev1 "github.com/ocelhq/ocel/pkg/proto/app/realtime/v1"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

func (s *Service) Mint(_ context.Context, req *realtimev1.MintRequest) (*realtimev1.MintResponse, error) {
	properties, err := s.readProperties(req.GetRealtime())
	if err != nil {
		return nil, err
	}
	key, err := readSigningKey(properties.GetSigningKey())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	issuedAt := time.Now().Truncate(time.Second)
	expiresAt := issuedAt.Add(req.GetTtl().AsDuration().Truncate(time.Second))
	sign := func(operation token.Operation, channel string) (string, error) {
		signed, err := signToken(key, token.Claims{
			Audience:  properties.GetHost(),
			IssuedAt:  issuedAt.Unix(),
			ExpiresAt: expiresAt.Unix(),
			Issuer:    "ocel:rt:" + req.GetRealtime(),
			Subject:   req.GetSubject(),
			Ocel:      token.Grant{Channel: channel, Namespace: req.GetRealtime(), Operation: operation},
		})
		if err != nil {
			return "", connect.NewError(connect.CodeInternal, err)
		}
		return signed, nil
	}
	answer := &realtimev1.MintResponse{SubscribeTokens: make([]*realtimev1.SubscribeToken, 0, len(req.GetSubscribeChannels())), ExpiresAt: timestamppb.New(expiresAt)}
	if req.GetConnect() {
		if answer.ConnectToken, err = sign(token.Connect, "/"+req.GetRealtime()); err != nil {
			return nil, err
		}
	}
	for _, channel := range req.GetSubscribeChannels() {
		signed, err := sign(token.Subscribe, channel)
		if err != nil {
			return nil, err
		}
		answer.SubscribeTokens = append(answer.SubscribeTokens, &realtimev1.SubscribeToken{Token: signed, Channel: channel})
	}
	return answer, nil
}

func readSigningKey(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("the realtime binding's signing key is no Ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func signToken(key ed25519.PrivateKey, claims token.Claims) (string, error) {
	id := make([]byte, 16)
	if _, err := cryptorand.Read(id); err != nil {
		return "", err
	}
	claims.ID = base64.RawURLEncoding.EncodeToString(id)
	return token.Sign(key, claims)
}
