package console

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

type Liveness string

const (
	Online         Liveness = "online"
	Offline        Liveness = "offline"
	NeverConnected Liveness = "never"
)

func LivenessOf(registered *consolev1.Connector) Liveness {
	switch {
	case registered.GetConnectedAt() == nil:
		return NeverConnected
	case registered.GetOnline():
		return Online
	default:
		return Offline
	}
}

type ConnectorAddress struct {
	URL       string
	PublicKey string
	Compute   string
}

var computeKinds = map[string]consolev1.ComputeKind{
	"serverless": consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS,
	"container":  consolev1.ComputeKind_COMPUTE_KIND_CONTAINER,
}

func ComputeNameOf(kind consolev1.ComputeKind) string {
	for name, known := range computeKinds {
		if known == kind {
			return name
		}
	}
	return ""
}

func (c *Client) connectors(accessToken string) consolev1connect.ConnectorServiceClient {
	return consolev1connect.NewConnectorServiceClient(c.http, c.baseURL+connectRoute, c.session(accessToken))
}

func (c *Client) ListConnectors(ctx context.Context, accessToken string) ([]*consolev1.Connector, error) {
	listed, err := c.connectors(accessToken).List(ctx, &consolev1.ListConnectorsRequest{})
	if err != nil {
		return nil, err
	}
	return listed.GetConnectors(), nil
}

func (c *Client) FindConnector(ctx context.Context, accessToken, target string) (*consolev1.Connector, error) {
	listed, err := c.ListConnectors(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(listed, func(row *consolev1.Connector) bool { return row.GetTarget() == target })
	if at < 0 {
		return nil, nil
	}
	return listed[at], nil
}

func (c *Client) UpsertConnector(ctx context.Context, accessToken, target, vendor string) (*consolev1.Connector, error) {
	registered, err := c.connectors(accessToken).Upsert(ctx, &consolev1.UpsertConnectorRequest{
		Target: target,
		Vendor: vendor,
		Reach:  consolev1.ConnectorReach_CONNECTOR_REACH_DIAL,
	})
	if err != nil {
		return nil, err
	}
	return registered.GetConnector(), nil
}

func (c *Client) SetConnectorAddress(ctx context.Context, accessToken, id string, address ConnectorAddress) (*consolev1.Connector, error) {
	request := &consolev1.SetConnectorAddressRequest{Id: id, Url: address.URL}
	if address.PublicKey != "" {
		request.PublicKey = proto.String(address.PublicKey)
	}
	if address.Compute != "" {
		kind, known := computeKinds[address.Compute]
		if !known {
			return nil, fmt.Errorf("the console has no compute kind %q", address.Compute)
		}
		request.Compute = kind
	}
	registered, err := c.connectors(accessToken).SetAddress(ctx, request)
	if err != nil {
		return nil, err
	}
	return registered.GetConnector(), nil
}

func (c *Client) RemoveConnector(ctx context.Context, accessToken, id string) error {
	_, err := c.connectors(accessToken).Remove(ctx, &consolev1.RemoveConnectorRequest{Id: id})
	return err
}
