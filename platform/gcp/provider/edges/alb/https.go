package alb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const httpsPort = "443"

type HTTPS struct{ Roots *x509.CertPool }

func (p *HTTPS) Warm(ctx context.Context, url, address, cookie string) error {
	asked, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	if cookie != "" {
		asked.Header.Set("Cookie", cookie)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: p.Roots}}
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
	answered, err := (&http.Client{Timeout: run.WarmTimeout, Transport: transport}).Do(asked)
	if err != nil {
		return fmt.Errorf("warm %s: %w", url, err)
	}
	return answered.Body.Close()
}
