package console

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const endpointTokenMargin = time.Minute

const CloudEndpointID = "ocel-cloud"

type EndpointToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *Client) IssueEndpointToken(ctx context.Context, accessToken, organizationID, endpointID string, scopes []scopev1.Scope) (EndpointToken, error) {
	issued, err := c.consoleService(accessToken).IssueEndpointToken(content.Omit(ctx), &consolev1.IssueEndpointTokenRequest{OrganizationId: organizationID, EndpointId: endpointID, Scopes: scopes})
	if err != nil {
		return EndpointToken{}, fmt.Errorf("have %s issue a token for endpoint %s: %w", c.baseURL, endpointID, err)
	}
	return EndpointToken{Token: issued.GetToken(), ExpiresAt: issued.GetExpiresAt().AsTime()}, nil
}

type EndpointTokens struct {
	client       *Client
	session      string
	organization string
	endpoint     string
	scopes       []scopev1.Scope
	now          func() time.Time

	mu   sync.Mutex
	held EndpointToken
}

func NewEndpointTokens(client *Client, session, organizationID, endpointID string, scopes ...scopev1.Scope) *EndpointTokens {
	return &EndpointTokens{client: client, session: session, organization: organizationID, endpoint: endpointID, scopes: slices.Clone(scopes), now: time.Now}
}

func (t *EndpointTokens) Ensure(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held.ExpiresAt.Sub(t.now()) > endpointTokenMargin {
		return t.held.Token, nil
	}
	issued, err := t.client.IssueEndpointToken(ctx, t.session, t.organization, t.endpoint, t.scopes)
	if err != nil {
		return "", err
	}
	t.held = issued
	return issued.Token, nil
}
