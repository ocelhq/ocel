package ports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"syscall"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials/idtoken"
	"golang.org/x/oauth2/google"

	"github.com/ocelhq/ocel/pkg/envsource"
)

const userLoginType = "authorized_user"

const (
	tokenAttempts = 4
	tokenBackoff  = 200 * time.Millisecond
	tokenCeiling  = 5 * time.Second
	tokenTimeout  = time.Minute
)

var tokenRetryAnswers = []int{
	http.StatusRequestTimeout,
	http.StatusTooManyRequests,
	http.StatusInternalServerError,
	http.StatusBadGateway,
	http.StatusServiceUnavailable,
	http.StatusGatewayTimeout,
}

func ProveIdentity(ctx context.Context, audience string) (envsource.IdentityProof, error) {
	credentials, err := idtoken.NewCredentials(&idtoken.Options{
		Audience: audience,
		Client:   &http.Client{Transport: retriedTransport{base: http.DefaultTransport}, Timeout: tokenTimeout},
	})
	if err != nil {
		if isUserLogin(ctx) {
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

func issued(ctx context.Context, credentials *auth.Credentials) (*auth.Token, error) {
	for attempt := 1; ; attempt++ {
		token, err := credentials.Token(ctx)
		if err == nil || attempt == tokenAttempts || !unanswered(err) {
			return token, err
		}
		if !waited(ctx, jittered(attempt)) {
			return nil, ctx.Err()
		}
	}
}

func unanswered(err error) bool {
	var refused *auth.Error
	if errors.As(err, &refused) {
		return refused.Temporary()
	}
	var reached net.Error
	return errors.As(err, &reached) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

func waited(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(min(wait, tokenCeiling))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
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
		if err != nil || attempt == tokenAttempts || !slices.Contains(tokenRetryAnswers, resp.StatusCode) ||
			(req.Body != nil && req.GetBody == nil) {
			return resp, err
		}
		wait := max(jittered(attempt), retryAfter(resp))
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if !waited(req.Context(), wait) {
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

func jittered(attempt int) time.Duration {
	backoff := min(tokenBackoff<<(attempt-1), tokenCeiling)
	return backoff/2 + rand.N(backoff/2)
}

func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func isUserLogin(ctx context.Context) bool {
	found, err := google.FindDefaultCredentials(ctx)
	if err != nil || len(found.JSON) == 0 {
		return false
	}
	var credential struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(found.JSON, &credential) == nil && credential.Type == userLoginType
}
