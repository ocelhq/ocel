package cloudflare

import (
	"context"
	"fmt"
	"net/http"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/zero_trust"

	"github.com/ocelhq/ocel/pkg/edge"
)

const tunnelDomain = ".cfargotunnel.com"

func tunnelOf(id string) edge.Tunnel { return edge.Tunnel{ID: id, Address: id + tunnelDomain} }

func (p *cloudflare) ensureTunnel(ctx context.Context, name string) (edge.Tunnel, error) {
	accountID, err := requireAccountID("open the tunnel " + name)
	if err != nil {
		return edge.Tunnel{}, err
	}
	if id, err := p.findTunnel(ctx, accountID, name); err != nil || id != "" {
		return tunnelOf(id), err
	}
	created, err := p.client.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(name),
		ConfigSrc: cf.F(zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare),
	})
	if err == nil {
		return tunnelOf(created.ID), nil
	}
	id, ferr := p.findTunnel(ctx, accountID, name)
	if ferr != nil || id == "" {
		return edge.Tunnel{}, fmt.Errorf("open the tunnel %s: %w", name, err)
	}
	return tunnelOf(id), nil
}

func (p *cloudflare) findTunnel(ctx context.Context, accountID, name string) (string, error) {
	listed := p.client.ZeroTrust.Tunnels.Cloudflared.ListAutoPaging(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cf.F(accountID),
		Name:      cf.F(name),
		IsDeleted: cf.F(false),
	})
	for listed.Next() {
		if held := listed.Current(); held.Name == name {
			return held.ID, nil
		}
	}
	if err := listed.Err(); err != nil {
		return "", fmt.Errorf("read the tunnel %s: %w", name, err)
	}
	return "", nil
}

func (p *cloudflare) configureTunnel(ctx context.Context, id, service string) error {
	accountID, err := requireAccountID("configure the tunnel " + id)
	if err != nil {
		return err
	}
	_, err = p.client.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, id, zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cf.F(accountID),
		Config: cf.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
			Ingress: cf.F([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{{Service: cf.F(service)}}),
		}),
	})
	if err != nil {
		return fmt.Errorf("send every hostname through the tunnel %s to %s: %w", id, service, err)
	}
	return nil
}

func (p *cloudflare) readTunnelToken(ctx context.Context, id string) (string, error) {
	accountID, err := requireAccountID("read the token of the tunnel " + id)
	if err != nil {
		return "", err
	}
	token, err := p.client.ZeroTrust.Tunnels.Cloudflared.Token.Get(ctx, id, zero_trust.TunnelCloudflaredTokenGetParams{AccountID: cf.F(accountID)})
	if err != nil {
		return "", fmt.Errorf("read the token of the tunnel %s: %w", id, err)
	}
	return *token, nil
}

func (p *cloudflare) deleteTunnel(ctx context.Context, id string) error {
	accountID, err := requireAccountID("delete the tunnel " + id)
	if err != nil {
		return err
	}
	_, err = p.client.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, id, zero_trust.TunnelCloudflaredConnectionDeleteParams{AccountID: cf.F(accountID)})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("clean up the connections of the tunnel %s: %w", id, err)
	}
	_, err = p.client.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, id, zero_trust.TunnelCloudflaredDeleteParams{AccountID: cf.F(accountID)})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("delete the tunnel %s: %w", id, err)
	}
	return nil
}
