package envidentity

import (
	"context"
	"encoding/json"
	"fmt"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"
)

const userLogin = "authorized_user"

type Issuer struct{}

func (Issuer) IDToken(ctx context.Context, audience string) (string, error) {
	source, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		if signedInAsUser(ctx) {
			return "", fmt.Errorf("this credential is a user's own login, and Google mints an identity token with an audience of ocel's choosing only for a service account: "+
				"deploy with a service account key, or run `gcloud auth application-default login --impersonate-service-account=<email>`: %w", err)
		}
		return "", fmt.Errorf("mint a Google identity token for %s: %w", audience, err)
	}
	token, err := source.Token()
	if err != nil {
		return "", fmt.Errorf("mint a Google identity token for %s: %w", audience, err)
	}
	return token.AccessToken, nil
}

func signedInAsUser(ctx context.Context) bool {
	found, err := google.FindDefaultCredentials(ctx)
	if err != nil || len(found.JSON) == 0 {
		return false
	}
	var held struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(found.JSON, &held) == nil && held.Type == userLogin
}
