package vps

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (p *Provider) Session(ctx context.Context) (*ssh.Session, error) {
	p.dial.Lock()
	defer p.dial.Unlock()
	if p.sshSession != nil {
		return p.sshSession, nil
	}
	target := p.options.SSH.session()
	if p.login != nil {
		certified, err := p.login.target(ctx)
		if err != nil {
			return nil, err
		}
		target = certified
	}
	live, err := ssh.Open(ctx, target)
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
	var closed error
	if live != nil {
		closed = live.Close()
	}
	if p.login != nil {
		closed = errors.Join(closed, p.login.close())
	}
	return closed
}
