package vps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
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
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		p.stopped(hostname, transport(err))
		return "", nil
	}
	defer said.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(said.Body, 1<<12))
	p.stopped(hostname, "")
	return edge.Kind(strings.TrimSpace(said.Header.Get(edge.HeaderEdge))), nil
}

func (p *Provider) Unreached(hostname string) string {
	p.probed.Lock()
	defer p.probed.Unlock()
	return p.unreached[hostname]
}

func (p *Provider) stopped(hostname, cause string) {
	p.probed.Lock()
	defer p.probed.Unlock()
	if cause == "" {
		delete(p.unreached, hostname)
		return
	}
	if p.unreached == nil {
		p.unreached = map[string]string{}
	}
	p.unreached[hostname] = cause
}

func transport(err error) string {
	var reached *url.Error
	if errors.As(err, &reached) {
		err = reached.Err
	}
	return err.Error()
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
