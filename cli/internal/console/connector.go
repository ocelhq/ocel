package console

import (
	"context"
	"net/http"
	"slices"
	"time"
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
	Online         Liveness = "online"
	Offline        Liveness = "offline"
	NeverConnected Liveness = "never"
)

func (c Connector) Liveness() Liveness {
	switch {
	case c.ConnectedAt == nil:
		return NeverConnected
	case c.Online:
		return Online
	default:
		return Offline
	}
}

type ConnectorRegistration struct {
	Target string `json:"target"`
	Vendor string `json:"vendor"`
	Reach  string `json:"reach"`
}

type ConnectorAddress struct {
	URL       string `json:"url"`
	PublicKey string `json:"publicKey,omitempty"`
	Compute   string `json:"compute,omitempty"`
}

const connectorsRoute = "/api/connectors"

func (c *Client) ListConnectors(ctx context.Context, accessToken string) ([]Connector, error) {
	var listed []Connector
	if err := c.send(ctx, http.MethodGet, connectorsRoute, accessToken, nil, &listed); err != nil {
		return nil, err
	}
	return listed, nil
}

func (c *Client) FindConnector(ctx context.Context, accessToken, target string) (*Connector, error) {
	listed, err := c.ListConnectors(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(listed, func(row Connector) bool { return row.Target == target })
	if at < 0 {
		return nil, nil
	}
	return &listed[at], nil
}

func (c *Client) UpsertConnector(ctx context.Context, accessToken string, registration ConnectorRegistration) (*Connector, error) {
	var registered Connector
	if err := c.send(ctx, http.MethodPut, connectorsRoute, accessToken, registration, &registered); err != nil {
		return nil, err
	}
	return &registered, nil
}

func (c *Client) SetConnectorAddress(ctx context.Context, accessToken, id string, address ConnectorAddress) (*Connector, error) {
	var registered Connector
	if err := c.send(ctx, http.MethodPatch, connectorsRoute+"/"+id, accessToken, address, &registered); err != nil {
		return nil, err
	}
	return &registered, nil
}

func (c *Client) RemoveConnector(ctx context.Context, accessToken, id string) error {
	return c.send(ctx, http.MethodDelete, connectorsRoute+"/"+id, accessToken, nil, nil)
}
