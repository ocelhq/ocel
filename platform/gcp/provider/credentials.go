package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

const tokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"

const credentialHint = "authenticate with Google Cloud: run `gcloud auth application-default login`"

const tokenInfoTimeout = 10 * time.Second

var tokenInfoClient = &http.Client{Timeout: tokenInfoTimeout}

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

	Endpoint string

	Projects ProjectReader
}

const emulatorPrincipal = "emulator"

func (c Credentials) Whoami(ctx context.Context) (providerkit.Identity, error) {
	identity := providerkit.Identity{
		Provider:  Vendor,
		Account:   c.Project,
		Principal: emulatorPrincipal,
		Location:  c.Region,
	}
	if c.Endpoint != "" {
		identity.Details = []providerkit.Detail{{Label: "emulator", Value: c.Endpoint}}
	} else {
		token, err := c.Tokens.Token(ctx)
		if err != nil {
			return providerkit.Identity{}, unauthenticated()
		}
		principal, err := c.principal(ctx, token)
		if err != nil {
			return providerkit.Identity{}, err
		}
		identity.Principal = principal
	}
	if c.Projects == nil {
		return providerkit.Identity{}, providerkit.Refuse(providerkit.CodeDenied,
			"nothing here can ask whether this credential reaches project %s, and a credential nothing vouched for deploys nothing", c.Project)
	}
	if err := c.Projects.Reaches(ctx, c.Project); err != nil {
		return providerkit.Identity{}, err
	}
	return identity, nil
}

func unauthenticated() error {
	return providerkit.Refuse(providerkit.CodeDenied, "%s", credentialHint)
}

func (c Credentials) principal(ctx context.Context, token string) (string, error) {
	endpoint := c.TokenInfoURL
	if endpoint == "" {
		endpoint = tokenInfoURL
	}
	principal, status, err := asked(ctx, func() (string, int, error) {
		return askTokenInfo(ctx, endpoint, token)
	})
	if err == nil && status == http.StatusOK {
		return principal, nil
	}
	if throttling(status) {
		return "", providerkit.Refuse(providerkit.CodeBusy,
			"Google's token endpoint is throttling or down: it answered neither a principal nor a refusal in %d attempts, and this credential may well be good", askAttempts)
	}
	return "", unauthenticated()
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

func (Credentials) Permissions(tier providerkit.CredentialTier) (edge.CredentialDocument, error) {
	switch tier {
	case providerkit.TierBootstrap, providerkit.TierDeploy:
		return edge.CredentialDocument{}, notReady("the permissions a credential needs")
	default:
		return edge.CredentialDocument{}, providerkit.Refuse(providerkit.CodeInvalid,
			"credential permissions are rendered for the bootstrap tier or the deploy tier; this request named neither")
	}
}

var _ providerkit.Credentials = Credentials{}
