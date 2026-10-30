package ports

import (
	"context"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

const UserinfoEmailScope = "https://www.googleapis.com/auth/userinfo.email"

type Credentials struct {
	externalAccount *google.Credentials
}

func NewExternalAccountCredentials(ctx context.Context, externalAccount []byte) (Credentials, error) {
	found, err := google.CredentialsFromJSONWithType(context.WithoutCancel(ctx), externalAccount, google.ExternalAccount, CloudPlatformScope, UserinfoEmailScope)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{externalAccount: found}, nil
}

func (c Credentials) Find(ctx context.Context) (*google.Credentials, error) {
	if c.externalAccount != nil {
		return c.externalAccount, nil
	}
	return google.FindDefaultCredentials(ctx, CloudPlatformScope, UserinfoEmailScope)
}

func (c Credentials) Token(ctx context.Context) (string, error) {
	found, err := c.Find(ctx)
	if err != nil {
		return "", err
	}
	token, err := found.TokenSource.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func (c Credentials) clientOptions() []option.ClientOption {
	if c.externalAccount == nil {
		return nil
	}
	return []option.ClientOption{option.WithCredentials(c.externalAccount)}
}
