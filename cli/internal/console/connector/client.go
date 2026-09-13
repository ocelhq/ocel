package connector

import (
	"context"
	"net/http"
	"time"

	"github.com/ocelhq/ocel/cli/internal/console/httpapi"
)

type Denial struct {
	Verb    string `json:"verb"`
	At      string `json:"at"`
	Message string `json:"message"`
}

type Connector struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organizationId"`
	Target         string     `json:"target"`
	Vendor         string     `json:"vendor"`
	Compute        *string    `json:"compute"`
	Reach          string     `json:"reach"`
	URL            *string    `json:"url"`
	PublicKey      *string    `json:"publicKey"`
	TLSPin         *string    `json:"tlsPin"`
	Version        *string    `json:"version"`
	Capabilities   []string   `json:"capabilities"`
	ConnectedAt    *time.Time `json:"connectedAt"`
	LastSeenAt     *time.Time `json:"lastSeenAt"`
	LastDenied     *Denial    `json:"lastDenied"`
}

type Liveness string

const (
	Online  Liveness = "online"
	Offline Liveness = "offline"
	Never   Liveness = "never"
)

const onlineWithin = 90 * time.Second

func (c Connector) Liveness(now time.Time) Liveness {
	switch {
	case c.ConnectedAt == nil:
		return Never
	case c.LastSeenAt == nil:
		return Offline
	case now.Sub(*c.LastSeenAt) < onlineWithin:
		return Online
	default:
		return Offline
	}
}

type Upsert struct {
	Target string `json:"target"`
	Vendor string `json:"vendor"`
	Reach  string `json:"reach"`
}

type Address struct {
	URL       string `json:"url"`
	PublicKey string `json:"publicKey,omitempty"`
	Compute   string `json:"compute,omitempty"`
}

type Client struct {
	api *httpapi.Client
}

func New(baseURL string) *Client {
	return &Client{api: httpapi.New(baseURL)}
}

func IsUnauthorized(err error) bool {
	return httpapi.HasStatus(err, http.StatusUnauthorized)
}

func IsNotFound(err error) bool {
	return httpapi.HasStatus(err, http.StatusNotFound)
}

const route = "/api/connectors"

func (c *Client) List(ctx context.Context, accessToken string) ([]Connector, error) {
	var held []Connector
	if err := c.api.Get(ctx, route, accessToken, &held); err != nil {
		return nil, err
	}
	return held, nil
}

func (c *Client) Upsert(ctx context.Context, accessToken string, taken Upsert) (*Connector, error) {
	var held Connector
	if err := c.api.Put(ctx, route, accessToken, taken, &held); err != nil {
		return nil, err
	}
	return &held, nil
}

func (c *Client) Address(ctx context.Context, accessToken, id string, at Address) (*Connector, error) {
	var held Connector
	if err := c.api.Patch(ctx, route+"/"+id, accessToken, at, &held); err != nil {
		return nil, err
	}
	return &held, nil
}

func (c *Client) Remove(ctx context.Context, accessToken, id string) error {
	return c.api.Delete(ctx, route+"/"+id, accessToken, nil)
}
