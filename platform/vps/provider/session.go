package vps

import (
	"context"

	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (p *Provider) Session(ctx context.Context) (*ssh.Session, error) {
	p.dial.Lock()
	defer p.dial.Unlock()
	if p.sshSession != nil {
		return p.sshSession, nil
	}
	live, err := ssh.Open(ctx, p.options.SSH.session())
	if err != nil {
		return nil, err
	}
	p.sshSession = live
	return p.sshSession, nil
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
	live := p.sshSession
	p.sshSession = nil
	if live == nil {
		return nil
	}
	return live.Close()
}
