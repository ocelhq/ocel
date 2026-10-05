package gcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
)

const httpsPort = "443"

func (p *Provider) warmThrough(ctx context.Context, url, address string) error {
	asked, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: p.warmRoots}}
	if address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			address = net.JoinHostPort(address, httpsPort)
		}
		var dialer net.Dialer
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}
	}
	defer transport.CloseIdleConnections()
	answered, err := (&http.Client{Timeout: warmTimeout, Transport: transport}).Do(asked)
	if err != nil {
		return fmt.Errorf("warm %s: %w", url, err)
	}
	return answered.Body.Close()
}
