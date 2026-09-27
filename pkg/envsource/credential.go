package envsource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Credential interface {
	logIn(ctx context.Context, client *infisicalClient) (session, error)
}

type session struct {
	token     string
	expiresAt time.Time
	fixed     bool
}

type SignedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type universalAuth struct{ clientID, clientSecret string }

func UniversalAuth(clientID, clientSecret string) Credential {
	return universalAuth{clientID: clientID, clientSecret: clientSecret}
}

func (u universalAuth) logIn(ctx context.Context, client *infisicalClient) (session, error) {
	return client.logIn(ctx, "universal-auth", map[string]string{"clientId": u.clientID, "clientSecret": u.clientSecret})
}

type IdentityProof struct {
	SignedRequest *SignedRequest
	IDToken       string
}

type identityAuth struct {
	identityID string
	prove      func(ctx context.Context, audience string) (IdentityProof, error)
}

func IdentityAuth(identityID string, proveIdentity func(ctx context.Context, audience string) (IdentityProof, error)) Credential {
	return identityAuth{identityID: identityID, prove: proveIdentity}
}

func (a identityAuth) logIn(ctx context.Context, client *infisicalClient) (session, error) {
	if a.prove == nil {
		return session{}, errors.New("identity auth logs in to Infisical as this target's own cloud identity, and this target has none")
	}
	proof, err := a.prove(ctx, a.identityID)
	if err != nil {
		return session{}, fmt.Errorf("prove this target's cloud identity to Infisical: %w", err)
	}
	switch {
	case proof.SignedRequest != nil && proof.IDToken == "":
		return logInWithSignedRequest(ctx, client, a.identityID, *proof.SignedRequest)
	case proof.IDToken != "" && proof.SignedRequest == nil:
		return client.logIn(ctx, "gcp-auth", map[string]string{"identityId": a.identityID, "jwt": proof.IDToken})
	}
	return session{}, errors.New("this target's cloud identity proved itself with neither one signed request nor one ID token, and Infisical accepts nothing else")
}

func logInWithSignedRequest(ctx context.Context, client *infisicalClient, identityID string, signed SignedRequest) (session, error) {
	endpoint, err := url.Parse(signed.URL)
	if err != nil {
		return session{}, fmt.Errorf("the signed request names no endpoint: %w", err)
	}
	headers := map[string]string{}
	for name, values := range signed.Header {
		if len(values) > 0 && !strings.EqualFold(name, "Content-Length") {
			headers[http.CanonicalHeaderKey(name)] = values[0]
		}
	}
	headers["Host"] = endpoint.Host
	headers["Content-Type"] = "application/x-www-form-urlencoded; charset=utf-8"
	headers["Content-Length"] = strconv.Itoa(len(signed.Body))
	encoded, err := json.Marshal(headers)
	if err != nil {
		return session{}, err
	}
	return client.logIn(ctx, "aws-auth", map[string]string{
		"identityId":           identityID,
		"iamHttpRequestMethod": signed.Method,
		"iamRequestBody":       base64.StdEncoding.EncodeToString(signed.Body),
		"iamRequestHeaders":    base64.StdEncoding.EncodeToString(encoded),
	})
}

type accessToken string

func AccessToken(token string) Credential { return accessToken(token) }

func (a accessToken) logIn(context.Context, *infisicalClient) (session, error) {
	return session{token: string(a), fixed: true}, nil
}
