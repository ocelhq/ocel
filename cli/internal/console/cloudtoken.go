package console

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const cloudTokenMargin = time.Minute

type CloudToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *Client) IssueCloudToken(ctx context.Context, accessToken, organizationID string, scopes []cloudv1.Scope) (CloudToken, error) {
	issued, err := c.consoleService(accessToken).IssueCloudToken(content.Omit(ctx), &consolev1.IssueCloudTokenRequest{OrganizationId: organizationID, Scopes: ScopeClaims(scopes)})
	if err != nil {
		return CloudToken{}, fmt.Errorf("have %s issue an Ocel Cloud token: %w", c.baseURL, err)
	}
	return CloudToken{Token: issued.GetToken(), ExpiresAt: issued.GetExpiresAt().AsTime()}, nil
}

func ScopeClaims(scopes []cloudv1.Scope) []string {
	claims := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		claims = append(claims, proto.GetExtension(scope.Descriptor().Values().ByNumber(scope.Number()).Options(), cloudv1.E_Claim).(string))
	}
	return claims
}

type CloudTokens struct {
	client       *Client
	session      string
	organization string
	scopes       []cloudv1.Scope
	now          func() time.Time

	mu   sync.Mutex
	held CloudToken
}

func NewCloudTokens(client *Client, session, organizationID string, scopes ...cloudv1.Scope) *CloudTokens {
	return &CloudTokens{client: client, session: session, organization: organizationID, scopes: slices.Clone(scopes), now: time.Now}
}

func (t *CloudTokens) Ensure(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held.ExpiresAt.Sub(t.now()) > cloudTokenMargin {
		return t.held.Token, nil
	}
	issued, err := t.client.IssueCloudToken(ctx, t.session, t.organization, t.scopes)
	if err != nil {
		return "", err
	}
	t.held = issued
	return issued.Token, nil
}
