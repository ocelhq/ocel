package gcloud

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

const TokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"

const credentialHint = "authenticate with Google Cloud: run `gcloud auth application-default login`"

const tokenInfoTimeout = 10 * time.Second

var tokenInfoClient = &http.Client{Transport: ports.NewGoogleTransport(nil), Timeout: tokenInfoTimeout}

type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

const EmulatorPrincipal = "emulator"

func Unauthenticated() error {
	return refusal.Refuse(refusal.CodeDenied, "%s", credentialHint)
}

func PrincipalNamed(ctx context.Context, endpoint, token string) (string, error) {
	principal, status, err := googleretry.Asked(ctx, func() (string, int, error) {
		return askTokenInfo(ctx, endpoint, token)
	})
	if err == nil && status == http.StatusOK {
		if principal == "" {
			return "", refusal.Refuse(refusal.CodeDenied,
				"this credential names no user or service account for ocel to grant roles to: authenticate as one, or have a workload identity impersonate a service account")
		}
		return principal, nil
	}
	if isThrottledOrDown(status) {
		return "", refusal.Refuse(refusal.CodeBusy,
			"Google's token endpoint is throttling or down: it answered neither a principal nor a refusal while ocel kept asking, and this credential may well be good")
	}
	return "", Unauthenticated()
}

func isThrottledOrDown(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func askTokenInfo(ctx context.Context, endpoint, token string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := tokenInfoClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, nil
	}
	var said struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&said); err != nil {
		return "", 0, err
	}
	return said.Email, resp.StatusCode, nil
}
