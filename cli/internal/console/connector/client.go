package connector

import (
	"context"
	"net/http"
	"slices"
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
	Online         bool       `json:"online"`
}

type Liveness string

const (
	Online  Liveness = "online"
	Offline Liveness = "offline"
	Never   Liveness = "never"
)

func (c Connector) Liveness() Liveness {
	switch {
	case c.ConnectedAt == nil:
		return Never
	case c.Online:
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
	var listed []Connector
	if err := c.api.Get(ctx, route, accessToken, &listed); err != nil {
		return nil, err
	}
	return listed, nil
}

func (c *Client) ByTarget(ctx context.Context, accessToken, fingerprint string) (*Connector, error) {
	listed, err := c.List(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(listed, func(row Connector) bool { return row.Target == fingerprint })
	if at < 0 {
		return nil, nil
	}
	return &listed[at], nil
}

func (c *Client) Upsert(ctx context.Context, accessToken string, taken Upsert) (*Connector, error) {
	var registered Connector
	if err := c.api.Put(ctx, route, accessToken, taken, &registered); err != nil {
		return nil, err
	}
	return &registered, nil
}

func (c *Client) Address(ctx context.Context, accessToken, id string, at Address) (*Connector, error) {
	var registered Connector
	if err := c.api.Patch(ctx, route+"/"+id, accessToken, at, &registered); err != nil {
		return nil, err
	}
	return &registered, nil
}

func (c *Client) Remove(ctx context.Context, accessToken, id string) error {
	return c.api.Delete(ctx, route+"/"+id, accessToken, nil)
}
