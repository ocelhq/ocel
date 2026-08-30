package vps

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const probeTimeout = 15 * time.Second

func (p *Provider) Serving(ctx context.Context, _ edge.Kind, hostname string) (edge.Kind, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://"+edge.ProbeHostname(hostname)+"/", nil)
	if err != nil {
		return "", err
	}
	said, err := p.probe().Do(request)
	if err != nil {
		return "", unreached(ctx, hostname, err)
	}
	defer said.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(said.Body, 1<<12))
	return edge.Kind(strings.TrimSpace(said.Header.Get(edge.HeaderEdge))), nil
}

func unreached(ctx context.Context, hostname string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var unverified *tls.CertificateVerificationError
	if errors.As(err, &unverified) {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s served a certificate this probe would not accept, and reading %s off a hostname is the proof a valid certificate was served for it: %v",
			hostname, edge.HeaderEdge, unverified)
	}
	return nil
}

func (p *Provider) probe() *http.Client {
	client := &http.Client{Timeout: probeTimeout}
	if p.probing != nil {
		held := *p.probing
		client = &held
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}
