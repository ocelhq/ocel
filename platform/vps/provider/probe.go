package vps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

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
		return "", nil
	}
	defer said.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(said.Body, 1<<12))
	return edge.Kind(strings.TrimSpace(said.Header.Get(edge.HeaderEdge))), nil
}

func (p *Provider) probe() *http.Client {
	if p.probing != nil {
		return p.probing
	}
	return &http.Client{Timeout: probeTimeout}
}
