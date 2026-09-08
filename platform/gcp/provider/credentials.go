package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"golang.org/x/oauth2/google"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

const tokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"

const credentialHint = "authenticate with Google Cloud: run `gcloud auth application-default login`, " +
	"or point GOOGLE_APPLICATION_CREDENTIALS at a service account key"

type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

type ApplicationDefault struct{}

func (ApplicationDefault) Token(ctx context.Context) (string, error) {
	found, err := google.FindDefaultCredentials(ctx, cloudPlatformScope)
	if err != nil {
		return "", err
	}
	token, err := found.TokenSource.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

type Credentials struct {
	Project string
	Region  string

	Tokens TokenSource

	TokenInfoURL string

	HTTP *http.Client
}

func (c Credentials) Whoami(ctx context.Context) (providerkit.Identity, error) {
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return providerkit.Identity{}, kit.Refuse(kit.CodeDenied, "%s", credentialHint)
	}
	principal, err := c.principal(ctx, token)
	if err != nil {
		return providerkit.Identity{}, kit.Refuse(kit.CodeDenied, "%s", credentialHint)
	}
	return providerkit.Identity{
		Provider:  Vendor,
		Account:   c.Project,
		Principal: principal,
		Location:  c.Region,
	}, nil
}

func (c Credentials) principal(ctx context.Context, token string) (string, error) {
	base := c.TokenInfoURL
	if base == "" {
		base = tokenInfoURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+url.Values{"access_token": {token}}.Encode(), nil)
	if err != nil {
		return "", err
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", kit.Refuse(kit.CodeDenied, "%s", credentialHint)
	}
	var said struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&said); err != nil {
		return "", err
	}
	return said.Email, nil
}

func (Credentials) Permissions(tier providerkit.CredentialTier) (edge.CredentialDocument, error) {
	switch tier {
	case providerkit.TierBootstrap, providerkit.TierDeploy:
		return edge.CredentialDocument{}, notReady("the permissions a credential needs")
	default:
		return edge.CredentialDocument{}, kit.Refuse(kit.CodeInvalid,
			"credential permissions are rendered for the bootstrap tier or the deploy tier; this request named neither")
	}
}

var _ providerkit.Credentials = Credentials{}
