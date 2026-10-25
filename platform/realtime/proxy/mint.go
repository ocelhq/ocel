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

var grantOperations = map[realtimev1.Operation]token.Operation{
	realtimev1.Operation_OPERATION_CONNECT:   token.Connect,
	realtimev1.Operation_OPERATION_SUBSCRIBE: token.Subscribe,
}

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
	answer := &realtimev1.MintResponse{Tokens: make([]*realtimev1.MintedToken, 0, len(req.GetGrants())), ExpiresAt: timestamppb.New(expiresAt)}
	for _, grant := range req.GetGrants() {
		operation, known := grantOperations[grant.GetOperation()]
		if !known {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a grant is to connect or to subscribe"))
		}
		signed, err := signToken(key, token.Claims{
			Audience:  properties.GetHost(),
			IssuedAt:  issuedAt.Unix(),
			ExpiresAt: expiresAt.Unix(),
			Issuer:    "ocel:rt:" + req.GetRealtime(),
			Subject:   req.GetSubject(),
			Ocel:      token.Grant{Channel: grant.GetChannel(), Namespace: req.GetRealtime(), Operation: operation},
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		answer.Tokens = append(answer.Tokens, &realtimev1.MintedToken{Token: signed, Channel: grant.GetChannel()})
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
