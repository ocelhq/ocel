package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func LivenessOf(registered *consolev1.Endpoint) Liveness {
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

func ComputeNameOf(kind consolev1.ComputeKind) string {
	switch kind {
	case consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS:
		return "serverless"
	case consolev1.ComputeKind_COMPUTE_KIND_CONTAINER:
		return "container"
	default:
		return ""
	}
}

func computeKindOf(name string) (consolev1.ComputeKind, bool) {
	switch name {
	case "serverless":
		return consolev1.ComputeKind_COMPUTE_KIND_SERVERLESS, true
	case "container":
		return consolev1.ComputeKind_COMPUTE_KIND_CONTAINER, true
	default:
		return consolev1.ComputeKind_COMPUTE_KIND_UNSPECIFIED, false
	}
}

func (c *Client) connectors(accessToken string) consolev1connect.EndpointServiceClient {
	return consolev1connect.NewEndpointServiceClient(c.http, c.baseURL+connectRoute, c.withSessionHeaders(accessToken))
}

func (c *Client) ListConnectors(ctx context.Context, accessToken string) ([]*consolev1.Endpoint, error) {
	listed, err := c.connectors(accessToken).List(ctx, &consolev1.ListEndpointsRequest{})
	if err != nil {
		return nil, err
	}
	return listed.GetEndpoints(), nil
}

func (c *Client) FindConnector(ctx context.Context, accessToken, target string) (*consolev1.Endpoint, error) {
	listed, err := c.ListConnectors(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(listed, func(row *consolev1.Endpoint) bool { return row.GetTarget() == target })
	if at < 0 {
		return nil, nil
	}
	return listed[at], nil
}

func (c *Client) UpsertConnector(ctx context.Context, accessToken, name, target, vendor string) (*consolev1.Endpoint, error) {
	registered, err := c.connectors(accessToken).Upsert(ctx, &consolev1.UpsertEndpointRequest{
		Name:   name,
		Target: target,
		Vendor: vendor,
		Reach:  consolev1.EndpointReach_ENDPOINT_REACH_DIAL,
	})
	if err != nil {
		return nil, err
	}
	return registered.GetEndpoint(), nil
}

func ConnectorNameOf(target string) string {
	sum := sha256.Sum256([]byte(target))
	return "connector-" + hex.EncodeToString(sum[:5])
}

func (c *Client) SetConnectorAddress(ctx context.Context, accessToken, id string, address ConnectorAddress) (*consolev1.Endpoint, error) {
	request := &consolev1.SetEndpointAddressRequest{Id: id, Url: address.URL}
	if address.PublicKey != "" {
		request.PublicKey = proto.String(address.PublicKey)
	}
	if address.Compute != "" {
		kind, known := computeKindOf(address.Compute)
		if !known {
			return nil, fmt.Errorf("the console has no compute kind %q", address.Compute)
		}
		request.Compute = kind
	}
	registered, err := c.connectors(accessToken).SetAddress(ctx, request)
	if err != nil {
		return nil, err
	}
	return registered.GetEndpoint(), nil
}

func (c *Client) RemoveConnector(ctx context.Context, accessToken, id string) error {
	_, err := c.connectors(accessToken).Remove(ctx, &consolev1.RemoveEndpointRequest{Id: id})
	return err
}
