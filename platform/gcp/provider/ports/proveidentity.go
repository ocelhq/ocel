package ports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/credentials/idtoken"

	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const userLoginType = "authorized_user"

const (
	retryAfterCeiling = 5 * time.Second
	tokenTimeout      = time.Minute
)

func (c Credentials) ProveIdentity(ctx context.Context, audience string) (envsource.IdentityProof, error) {
	credentials, err := c.idTokens(audience)
	if err != nil {
		if c.isUserLogin(ctx) {
			return envsource.IdentityProof{}, fmt.Errorf("this credential is a user's own login, and Google issues an identity token with an audience of ocel's choosing only to a service account: "+
				"deploy with a service account key, or run `gcloud auth application-default login --impersonate-service-account=<email>`: %w", err)
		}
		return envsource.IdentityProof{}, fmt.Errorf("issue a Google identity token for %s: %w", audience, err)
	}
	token, err := issued(ctx, credentials)
	if err != nil {
		return envsource.IdentityProof{}, fmt.Errorf("issue a Google identity token for %s: %w", audience, err)
	}
	return envsource.IdentityProof{IDToken: token.Value}, nil
}

func (c Credentials) idTokens(audience string) (*auth.Credentials, error) {
	options := &idtoken.Options{Audience: audience, Client: newTokenClient()}
	if c.externalAccount == nil {
		return idtoken.NewCredentials(options)
	}
	return idtoken.NewCredentialsFromJSON(credentials.ExternalAccount, c.externalAccount.JSON, options)
}

func issued(ctx context.Context, credentials *auth.Credentials) (*auth.Token, error) {
	token, _, err := googleretry.Asked(ctx, func() (*auth.Token, int, error) {
		token, err := credentials.Token(ctx)
		return token, refusedCode(err), err
	})
	return token, err
}

func refusedCode(err error) int {
	var refused *auth.Error
	if errors.As(err, &refused) && refused.Response != nil {
		return refused.Response.StatusCode
	}
	return 0
}

func newTokenClient() *http.Client {
	return &http.Client{Transport: retriedTransport{base: NewGoogleTransport(http.DefaultTransport)}, Timeout: tokenTimeout}
}

type retriedTransport struct {
	base http.RoundTripper
}

func (t retriedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		sent, err := rewound(req, attempt)
		if err != nil {
			return nil, err
		}
		resp, err := t.base.RoundTrip(sent)
		if err != nil || attempt == googleretry.Attempts || !googleretry.IsRetryableAnswer(resp.StatusCode) ||
			(req.Body != nil && req.GetBody == nil) {
			return resp, err
		}
		after := retryAfter(resp)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if !waitedToRetry(req.Context(), attempt, after) {
			return nil, req.Context().Err()
		}
	}
}

func rewound(req *http.Request, attempt int) (*http.Request, error) {
	if attempt == 1 || req.GetBody == nil {
		return req, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	again := req.Clone(req.Context())
	again.Body = body
	return again, nil
}

func waitedToRetry(ctx context.Context, attempt int, after time.Duration) bool {
	if after == 0 {
		return googleretry.Waited(ctx, attempt)
	}
	timer := time.NewTimer(min(after, retryAfterCeiling))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (c Credentials) isUserLogin(ctx context.Context) bool {
	found, err := c.Find(ctx)
	if err != nil || len(found.JSON) == 0 {
		return false
	}
	var credential struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(found.JSON, &credential) == nil && credential.Type == userLoginType
}
