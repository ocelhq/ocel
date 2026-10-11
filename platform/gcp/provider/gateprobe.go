package gcp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

const (
	gateProbeQuery        = "/?ocel-gate-probe="
	gateProbeBodyMaxBytes = 16 << 10
)

func (p *Provider) probeGate(ctx context.Context, hostname, address string) (bool, error) {
	target := gateProbeQuery + rand.Text()
	asked, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+target, nil)
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
	if answered.StatusCode != http.StatusUnauthorized ||
		answered.Header.Get("Cache-Control") != previewgate.CacheControl ||
		answered.Header.Get("X-Robots-Tag") != previewgate.RobotsTag {
		return false, nil
	}
	body, err := io.ReadAll(io.LimitReader(answered.Body, gateProbeBodyMaxBytes))
	if err != nil {
		return false, fmt.Errorf("read %s's answer to whether its preview gate is enforced: %w", hostname, err)
	}
	return strings.Contains(string(body), `action="`+previewgate.LoginPath+`"`) &&
		strings.Contains(string(body), `value="`+target+`"`), nil
}
