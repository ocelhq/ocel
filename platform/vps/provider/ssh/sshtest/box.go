package sshtest

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Box struct {
	Facts  ssh.Facts
	User   string
	Answer func(command string) (ssh.Result, bool)

	mu  sync.Mutex
	ran []string
	fed []string
}

func New(answer func(command string) (ssh.Result, bool)) *Box {
	return &Box{Facts: ssh.Facts{Root: true, Systemd: true, Arch: "x86_64"}, User: "ada", Answer: answer}
}

func (b *Box) Open() *ssh.Box { return ssh.NewBox(b.Dial, nil) }

func (b *Box) Dial(ctx context.Context) (ssh.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return conn{b}, nil
}

func (b *Box) Commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ran...)
}

func (b *Box) Fed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.fed...)
}

type conn struct{ b *Box }

func (c conn) Destination() ssh.Destination {
	return ssh.Destination{Address: "203.0.113.10", Port: 22, User: c.b.User}
}

func (c conn) Preflight(context.Context) (ssh.Facts, error) {
	if !c.b.Facts.Root && !c.b.Facts.Sudo {
		return c.b.Facts, refusal.Refuse(refusal.CodeDenied, "%s is neither root nor has passwordless sudo", c.b.User)
	}
	return c.b.Facts, nil
}

func (c conn) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("this box forwards no port")
}

func (c conn) Run(ctx context.Context, command string) (string, error) {
	result, err := c.Stream(ctx, command, nil)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", errors.New(result.Stderr)
	}
	return result.Stdout, nil
}

func (c conn) Stream(_ context.Context, command string, stdin io.Reader) (ssh.Result, error) {
	var fed []byte
	if stdin != nil {
		read, err := io.ReadAll(stdin)
		if err != nil {
			return ssh.Result{}, err
		}
		fed = read
	}
	c.b.mu.Lock()
	c.b.ran = append(c.b.ran, command)
	c.b.fed = append(c.b.fed, string(fed))
	answer := c.b.Answer
	c.b.mu.Unlock()
	if answer != nil {
		if scripted, said := answer(command); said {
			return scripted, nil
		}
	}
	if strings.Contains(command, "id -u") && c.b.Facts.Root {
		return ssh.Result{Stdout: "0\n"}, nil
	}
	if strings.Contains(command, "id -u") {
		return ssh.Result{Stdout: "1001\n"}, nil
	}
	return ssh.Result{}, nil
}

func (c conn) RunLines(ctx context.Context, command string, each func(ssh.Line) error) error {
	result, err := c.Stream(ctx, command, nil)
	if err != nil {
		return err
	}
	for line := range strings.Lines(result.Stdout) {
		if err := each(ssh.Line{Pipe: ssh.Stdout, Text: strings.TrimSuffix(line, "\n")}); err != nil {
			return err
		}
	}
	return nil
}
