package console

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const cloudTokenMargin = time.Minute

type CloudToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *Client) IssueCloudToken(ctx context.Context, accessToken string, scopes []string) (CloudToken, error) {
	issued, err := c.consoleService(accessToken).IssueCloudToken(ctx, &consolev1.IssueCloudTokenRequest{Scopes: scopes})
	if err != nil {
		return CloudToken{}, fmt.Errorf("have %s issue an Ocel Cloud token: %w", c.baseURL, err)
	}
	return CloudToken{Token: issued.GetToken(), ExpiresAt: issued.GetExpiresAt().AsTime()}, nil
}

type CloudTokens struct {
	client  *Client
	session string
	scopes  []string
	now     func() time.Time

	mu   sync.Mutex
	held CloudToken
}

func NewCloudTokens(client *Client, session string, scopes ...string) *CloudTokens {
	return &CloudTokens{client: client, session: session, scopes: slices.Clone(scopes), now: time.Now}
}

func (t *CloudTokens) Ensure(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held.ExpiresAt.Sub(t.now()) > cloudTokenMargin {
		return t.held.Token, nil
	}
	issued, err := t.client.IssueCloudToken(ctx, t.session, t.scopes)
	if err != nil {
		return "", err
	}
	t.held = issued
	return issued.Token, nil
}
