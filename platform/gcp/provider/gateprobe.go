package gcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

func (p *Provider) probeGate(ctx context.Context, hostname, address string) (bool, error) {
	asked, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+"/", nil)
	if err != nil {
		return false, err
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
	client := &http.Client{
		Timeout:       warmTimeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	answered, err := client.Do(asked)
	if err != nil {
		return false, fmt.Errorf("ask %s whether its preview gate is enforced: %w", hostname, err)
	}
	defer answered.Body.Close()
	return answered.StatusCode == http.StatusUnauthorized &&
		answered.Header.Get("Cache-Control") == previewgate.CacheControl &&
		answered.Header.Get("X-Robots-Tag") == previewgate.RobotsTag, nil
}
