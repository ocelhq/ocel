package vps

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (p *Provider) Session(ctx context.Context) (*ssh.Session, error) {
	p.dial.Lock()
	defer p.dial.Unlock()
	if p.live != nil {
		return p.live, nil
	}
	live, err := ssh.Open(ctx, p.options.SSH.session())
	if err != nil {
		return nil, err
	}
	p.live = live
	return p.live, nil
}

func (p *Provider) conn(ctx context.Context) (ssh.Conn, error) {
	live, err := p.Session(ctx)
	if err != nil {
		return nil, err
	}
	return live, nil
}

func (p *Provider) Close() error {
	p.dial.Lock()
	defer p.dial.Unlock()
	live := p.live
	p.live = nil
	if live == nil {
		return nil
	}
	return live.Close()
}
